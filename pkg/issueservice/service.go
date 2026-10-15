// Package issueservice exposes local issue archives through authenticated server routes.
package issueservice

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
	"github.com/ImSingee/git-plus/pkg/issuetask"
	issuev1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1/issuev1connect"
	taskv1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/task/v1"
	"github.com/ImSingee/git-plus/pkg/task"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	DataDir string
	DB      *sql.DB
	Runner  *issuetask.Runner
}

func New(dataDir string, db *sql.DB, manager *task.Manager) *Service {
	return &Service{DataDir: dataDir, DB: db, Runner: issuetask.New(dataDir, db, manager)}
}
func RegisterHandlers(mux *http.ServeMux, dataDir string, db *sql.DB, manager *task.Manager) {
	s := New(dataDir, db, manager)
	interceptor, err := connectvalidate.NewInterceptor()
	if err != nil {
		panic(err)
	}
	path, handler := issuev1connect.NewIssueServiceHandler(s, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	mux.HandleFunc("GET /repos/{repoID}/issues/export", s.export)
}
func (s *Service) repo(ctx context.Context, id int64) (dbsqlc.Repo, error) {
	if id <= 0 {
		return dbsqlc.Repo{}, connect.NewError(connect.CodeInvalidArgument, errors.New("repo_id must be positive"))
	}
	repo, err := dbsqlc.New(s.DB).GetRepoById(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return repo, connect.NewError(connect.CodeNotFound, errors.New("repository not found"))
	}
	if err != nil {
		return repo, rpcError(err)
	}
	return repo, nil
}
func (s *Service) store(repo dbsqlc.Repo) (*issuearchive.Store, error) {
	path, err := issuearchive.Path(s.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		return nil, err
	}
	return issuearchive.NewStore(path)
}
func (s *Service) GetStatus(ctx context.Context, req *connect.Request[issuev1.GetStatusRequest]) (*connect.Response[issuev1.GetStatusResponse], error) {
	repo, err := s.repo(ctx, req.Msg.GetRepoId())
	if err != nil {
		return nil, err
	}
	enabled, err := issuetask.IsEnabled(s.DataDir, repo.SourceID)
	if err != nil {
		return nil, rpcError(err)
	}
	state, err := dbsqlc.New(s.DB).GetIssueSyncState(ctx, repo.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, rpcError(err)
	}
	status := state.Status
	if status == "" {
		status = "never"
	}
	return connect.NewResponse(&issuev1.GetStatusResponse{Status: &issuev1.IssueBackupStatus{
		Enabled: proto.Bool(enabled), State: proto.String(status), LastSuccessfulAt: proto.String(state.LastSuccessfulAt),
		LastFullReconcileAt: proto.String(state.LastFullReconcileAt), LastError: proto.String(state.LastError), LatestCommit: proto.String(state.LatestCommit),
		IssueCount: proto.Int32(int32(state.IssueCount)), CommentCount: proto.Int32(int32(state.CommentCount)),
	}}), nil
}

type pageCursor struct {
	RepoID        int64
	State, Search string
	Offset        int64
}

func (s *Service) ListIssues(ctx context.Context, req *connect.Request[issuev1.ListIssuesRequest]) (*connect.Response[issuev1.ListIssuesResponse], error) {
	repo, err := s.repo(ctx, req.Msg.GetRepoId())
	if err != nil {
		return nil, err
	}
	state, search := req.Msg.GetState(), req.Msg.GetSearch()
	if state != "" && state != "open" && state != "closed" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid issue state"))
	}
	if len(search) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("search is too long"))
	}
	cursor := pageCursor{RepoID: repo.ID, State: state, Search: search}
	if token := req.Msg.GetPageToken(); token != "" {
		cursor = pageCursor{}
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.RepoID != repo.ID || cursor.State != state || cursor.Search != search || cursor.Offset < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page token"))
		}
	}
	size := int64(req.Msg.GetPageSize())
	if size <= 0 {
		size = 30
	}
	if size > 100 {
		size = 100
	}
	q := dbsqlc.New(s.DB)
	total, err := q.CountIssuesCurrent(ctx, dbsqlc.CountIssuesCurrentParams{RepoID: repo.ID, Column2: state, Column3: search})
	if err != nil {
		return nil, rpcError(err)
	}
	rows, err := q.ListIssuesCurrent(ctx, dbsqlc.ListIssuesCurrentParams{RepoID: repo.ID, Column2: state, Column3: search, Limit: size, Offset: cursor.Offset})
	if err != nil {
		return nil, rpcError(err)
	}
	result := &issuev1.ListIssuesResponse{TotalCount: proto.Int32(int32(total)), Issues: make([]*issuev1.IssueSummary, 0, len(rows))}
	for _, row := range rows {
		result.Issues = append(result.Issues, summary(row))
	}
	cursor.Offset += int64(len(rows))
	if cursor.Offset < total {
		b, _ := json.Marshal(cursor)
		result.NextPageToken = proto.String(base64.RawURLEncoding.EncodeToString(b))
	}
	return connect.NewResponse(result), nil
}
func summary(row dbsqlc.IssuesCurrent) *issuev1.IssueSummary {
	var labels, assignees []string
	_ = json.Unmarshal([]byte(row.LabelsJson), &labels)
	_ = json.Unmarshal([]byte(row.AssigneesJson), &assignees)
	return &issuev1.IssueSummary{Id: proto.String(row.IssueID), Number: proto.Int64(row.Number), Title: proto.String(row.Title), State: proto.String(row.State), AuthorLogin: proto.String(row.AuthorLogin), UpdatedAt: proto.String(row.UpdatedAt), Availability: proto.String(row.Availability), Labels: labels, Assignees: assignees}
}
func archivedSummary(issue issuearchive.Issue) (*issuev1.IssueSummary, error) {
	var f struct {
		Title, State string
		UpdatedAt    string `json:"updated_at"`
		User         struct{ Login string }
		Labels       []struct{ Name string }
		Assignees    []struct{ Login string }
	}
	if err := json.Unmarshal(issue.Raw, &f); err != nil {
		return nil, err
	}
	out := &issuev1.IssueSummary{Id: proto.String(issue.ID), Number: proto.Int64(issue.Number), Title: proto.String(f.Title), State: proto.String(f.State), AuthorLogin: proto.String(f.User.Login), UpdatedAt: proto.String(f.UpdatedAt), Availability: proto.String(issue.Availability)}
	for _, label := range f.Labels {
		out.Labels = append(out.Labels, label.Name)
	}
	for _, assignee := range f.Assignees {
		out.Assignees = append(out.Assignees, assignee.Login)
	}
	return out, nil
}
func (s *Service) GetIssue(ctx context.Context, req *connect.Request[issuev1.GetIssueRequest]) (*connect.Response[issuev1.GetIssueResponse], error) {
	repo, err := s.repo(ctx, req.Msg.GetRepoId())
	if err != nil {
		return nil, err
	}
	store, err := s.store(repo)
	if err != nil {
		return nil, rpcError(err)
	}
	revision := req.Msg.GetSnapshot()
	if revision == "" {
		history, err := store.History(ctx, 1)
		if err != nil {
			return nil, rpcError(err)
		}
		if len(history) == 0 {
			return nil, rpcError(issuearchive.ErrNoSnapshots)
		}
		revision = history[0].Commit
	}
	snapshot, err := store.Load(ctx, revision)
	if err != nil {
		return nil, rpcError(err)
	}
	issue, ok := snapshot.Issues[req.Msg.GetIssueId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("issue not present in snapshot"))
	}
	item, err := archivedSummary(issue)
	if err != nil {
		return nil, rpcError(err)
	}
	comments, _ := json.Marshal(issue.Comments)
	timeline, _ := json.Marshal(issue.Timeline)
	markdown, err := issuearchive.RenderMarkdown(issue)
	if err != nil {
		return nil, rpcError(err)
	}
	return connect.NewResponse(&issuev1.GetIssueResponse{Issue: item, RawJson: proto.String(string(issue.Raw)), CommentsJson: proto.String(string(comments)), TimelineJson: proto.String(string(timeline)), Markdown: proto.String(markdown), Snapshot: proto.String(revision)}), nil
}
func (s *Service) ListSnapshots(ctx context.Context, req *connect.Request[issuev1.ListSnapshotsRequest]) (*connect.Response[issuev1.ListSnapshotsResponse], error) {
	repo, err := s.repo(ctx, req.Msg.GetRepoId())
	if err != nil {
		return nil, err
	}
	store, err := s.store(repo)
	if err != nil {
		return nil, rpcError(err)
	}
	history, err := store.History(ctx, 100)
	if err != nil && !errors.Is(err, issuearchive.ErrNoSnapshots) {
		return nil, rpcError(err)
	}
	out := &issuev1.ListSnapshotsResponse{}
	for _, snapshot := range history {
		out.Snapshots = append(out.Snapshots, &issuev1.IssueSnapshot{Commit: proto.String(snapshot.Commit), CreatedAt: proto.String(snapshot.CreatedAt.UTC().Format(time.RFC3339Nano)), IssueCount: proto.Int32(int32(snapshot.IssueCount)), CommentCount: proto.Int32(int32(snapshot.CommentCount))})
	}
	return connect.NewResponse(out), nil
}
func (s *Service) EnqueueSync(ctx context.Context, req *connect.Request[issuev1.EnqueueSyncRequest]) (*connect.Response[issuev1.EnqueueSyncResponse], error) {
	if _, err := s.repo(ctx, req.Msg.GetRepoId()); err != nil {
		return nil, err
	}
	result, item, err := s.Runner.Enqueue(ctx, req.Msg.GetRepoId(), req.Msg.GetFull(), "")
	if err != nil {
		return nil, rpcError(err)
	}
	state := taskv1.TaskState_TASK_STATE_QUEUED
	if item.State == task.StateRunning {
		state = taskv1.TaskState_TASK_STATE_RUNNING
	}
	enqueue := taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_QUEUED
	if result == task.EnqueueResultStarted {
		enqueue = taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_STARTED
	}
	if result == task.EnqueueResultDeduped {
		enqueue = taskv1.TaskEnqueueResult_TASK_ENQUEUE_RESULT_DEDUPED
	}
	return connect.NewResponse(&issuev1.EnqueueSyncResponse{Result: &enqueue, Task: &taskv1.Task{TaskId: proto.String(item.TaskID), JobId: proto.String(item.JobID), JobType: proto.String(item.JobType), Name: proto.String(item.Name), State: &state, CreatedAt: timestamppb.New(item.CreatedAt)}}), nil
}
func (s *Service) export(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("repoID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid repository", http.StatusBadRequest)
		return
	}
	repo, err := s.repo(r.Context(), id)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}
	store, err := s.store(repo)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	file, err := os.CreateTemp("", "git-plus-issues-*.zip")
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := store.ExportZIP(r.Context(), file, r.URL.Query().Get("snapshot")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, issuearchive.ErrNoSnapshots) {
			status = http.StatusNotFound
		}
		if errors.Is(err, issuearchive.ErrInvalidRevision) {
			status = http.StatusBadRequest
		}
		http.Error(w, "issue archive export failed", status)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="issues-%d.zip"`, id))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, file)
}
func rpcError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, issuearchive.ErrNoSnapshots):
		code = connect.CodeNotFound
	case errors.Is(err, issuearchive.ErrInvalidRevision):
		code = connect.CodeInvalidArgument
	case errors.Is(err, issuetask.ErrDisabled):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, sql.ErrNoRows):
		code = connect.CodeNotFound
	}
	return connect.NewError(code, err)
}
