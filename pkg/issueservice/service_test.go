package issueservice

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
	"github.com/ImSingee/git-plus/pkg/issuesync"
	issuev1 "github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1"
	"github.com/ImSingee/git-plus/pkg/rpc/gitplus/issue/v1/issuev1connect"
	"github.com/ImSingee/git-plus/pkg/task"
	"google.golang.org/protobuf/proto"
)

func fixture(t *testing.T) (*Service, dbsqlc.Repo, *issuearchive.Store) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := appdb.Migrate(ctx, dir); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := "2026-10-15T12:00:00Z"
	if err := dbsqlc.New(db).CreateRepo(ctx, dbsqlc.CreateRepoParams{SourceID: "test", Platform: "github", RefID: "123", Status: "active", Name: "repo", FullName: "owner/repo", Owner: "owner", Origin: `["default"]`, Meta: "{}", LastSeenAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repos, err := dbsqlc.New(db).ListReposForSource(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := appconfig.Save(appconfig.PathForDataDir(dir), appconfig.Config{Sources: []appconfig.SourceConfig{{ID: "test", Platform: "github", Username: "owner", IssuesEnabled: false}}}); err != nil {
		t.Fatal(err)
	}
	path, err := issuearchive.Path(dir, "test", "123")
	if err != nil {
		t.Fatal(err)
	}
	store, err := issuearchive.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := issuearchive.Snapshot{Repository: issuearchive.Repository{SourceID: "test", ID: "123", FullName: "owner/repo"}, Issues: map[string]issuearchive.Issue{
		"9007199254740993": {ID: "9007199254740993", Number: 1, Raw: json.RawMessage(`{"id":9007199254740993,"number":1,"title":"First <script>","state":"open","body":"<script>alert(1)</script>","user":{"login":"author"},"labels":[{"name":"bug"}],"assignees":[{"login":"owner"}]}`), Comments: []json.RawMessage{json.RawMessage(`{"id":10,"body":"comment"}`)}, Timeline: []json.RawMessage{}, Availability: "available"},
		"9007199254740994": {ID: "9007199254740994", Number: 2, Raw: json.RawMessage(`{"id":9007199254740994,"number":2,"title":"Second","state":"closed"}`), Comments: []json.RawMessage{}, Timeline: []json.RawMessage{}, Availability: "unavailable"},
	}, Labels: []json.RawMessage{}, Milestones: []json.RawMessage{}, APIVersion: "2022-11-28", StartedAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 10, 15, 12, 1, 0, 0, time.UTC), IssuesSince: now, CommentsSince: now, LastFullReconcileAt: now, Full: false}
	result, err := store.Save(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := issuesync.New(dir, db).IndexSnapshot(ctx, repos[0], snapshot, result.Commit); err != nil {
		t.Fatal(err)
	}
	manager := task.NewManager()
	t.Cleanup(manager.Close)
	return New(dir, db, manager), repos[0], store
}
func TestReadSavedIssuesWhileDisabled(t *testing.T) {
	s, repo, store := fixture(t)
	ctx := context.Background()
	status, err := s.GetStatus(ctx, connect.NewRequest(&issuev1.GetStatusRequest{RepoId: proto.Int64(repo.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	if status.Msg.GetStatus().GetEnabled() || status.Msg.GetStatus().GetIssueCount() != 2 {
		t.Fatalf("wrong status: %v", status.Msg)
	}
	first, err := s.ListIssues(ctx, connect.NewRequest(&issuev1.ListIssuesRequest{RepoId: proto.Int64(repo.ID), PageSize: proto.Int32(1)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Issues) != 1 || first.Msg.Issues[0].GetNumber() != 2 || first.Msg.GetNextPageToken() == "" {
		t.Fatalf("wrong page: %v", first.Msg)
	}
	second, err := s.ListIssues(ctx, connect.NewRequest(&issuev1.ListIssuesRequest{RepoId: proto.Int64(repo.ID), PageToken: first.Msg.NextPageToken, PageSize: proto.Int32(1)}))
	if err != nil {
		t.Fatal(err)
	}
	if second.Msg.Issues[0].GetId() != "9007199254740993" || second.Msg.GetNextPageToken() != "" {
		t.Fatalf("wrong second page: %v", second.Msg)
	}
	filtered, err := s.ListIssues(ctx, connect.NewRequest(&issuev1.ListIssuesRequest{RepoId: proto.Int64(repo.ID), State: proto.String("open"), Search: proto.String("First")}))
	if err != nil || filtered.Msg.GetTotalCount() != 1 {
		t.Fatalf("filter: %v %v", filtered, err)
	}
	history, err := store.History(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetIssue(ctx, connect.NewRequest(&issuev1.GetIssueRequest{RepoId: proto.Int64(repo.ID), IssueId: proto.String("9007199254740993"), Snapshot: proto.String(history[0].Commit)}))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Msg.GetIssue().GetId() != "9007199254740993" || !bytes.Contains([]byte(detail.Msg.GetRawJson()), []byte("9007199254740993")) || detail.Msg.GetCommentsJson() == "[]" || detail.Msg.GetSnapshot() != history[0].Commit {
		t.Fatalf("wrong detail: %v", detail.Msg)
	}
	snapshots, err := s.ListSnapshots(ctx, connect.NewRequest(&issuev1.ListSnapshotsRequest{RepoId: proto.Int64(repo.ID)}))
	if err != nil || len(snapshots.Msg.Snapshots) != 1 {
		t.Fatalf("snapshots: %v %v", snapshots, err)
	}
	_, err = s.EnqueueSync(ctx, connect.NewRequest(&issuev1.EnqueueSyncRequest{RepoId: proto.Int64(repo.ID)}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("disabled sync: %v", err)
	}
}
func TestInvalidRequestsAndVerifiedExport(t *testing.T) {
	s, repo, _ := fixture(t)
	ctx := context.Background()
	for _, token := range []string{"not base64!", "e30"} {
		_, err := s.ListIssues(ctx, connect.NewRequest(&issuev1.ListIssuesRequest{RepoId: proto.Int64(repo.ID), PageToken: proto.String(token)}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("token %q: %v", token, err)
		}
	}
	_, err := s.GetIssue(ctx, connect.NewRequest(&issuev1.GetIssueRequest{RepoId: proto.Int64(repo.ID), IssueId: proto.String("9007199254740993"), Snapshot: proto.String("../../HEAD")}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("revision: %v", err)
	}
	_, err = s.GetIssue(ctx, connect.NewRequest(&issuev1.GetIssueRequest{RepoId: proto.Int64(repo.ID), IssueId: proto.String("missing")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	mux := http.NewServeMux()
	RegisterHandlers(mux, s.DataDir, s.DB, s.Runner.Manager)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/repos/1/issues/export", nil))
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/zip" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export: %d %s", response.Code, response.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range reader.File {
		if f.Name == "manifest.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing manifest")
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/repos/1/issues/export?snapshot=HEAD", nil))
	if response.Code != 400 {
		t.Fatalf("invalid export revision: %d", response.Code)
	}
}
func TestConnectRoundTrip(t *testing.T) {
	s, repo, _ := fixture(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s.DataDir, s.DB, s.Runner.Manager)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := issuev1connect.NewIssueServiceClient(server.Client(), server.URL)
	response, err := client.ListIssues(context.Background(), connect.NewRequest(&issuev1.ListIssuesRequest{RepoId: proto.Int64(repo.ID)}))
	if err != nil || len(response.Msg.GetIssues()) != 2 {
		t.Fatalf("RPC: %v %v", response, err)
	}
	_, err = client.GetStatus(context.Background(), connect.NewRequest(&issuev1.GetStatusRequest{RepoId: proto.Int64(-1)}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("validation: %v", err)
	}
}
