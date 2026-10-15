// Package issuesync synchronizes observable GitHub issue state to a local archive.
package issuesync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	"github.com/ImSingee/git-plus/pkg/githubapi"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
)

const ReconcileInterval = 7 * 24 * time.Hour
const Overlap = 5 * time.Minute

var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)
var repoLocks sync.Map

type Reporter interface {
	SetProgress(string, map[string]any) error
}
type Request struct {
	Repo  dbsqlc.Repo
	Token string
	Full  bool
}
type Engine struct {
	DataDir string
	DB      *sql.DB
	Client  *githubapi.Client
	Now     func() time.Time
}

func New(dataDir string, db *sql.DB) *Engine {
	return &Engine{DataDir: dataDir, DB: db, Client: githubapi.New("", nil), Now: time.Now}
}

type issueFields struct {
	ID            json.Number     `json:"id"`
	Number        int64           `json:"number"`
	NodeID        string          `json:"node_id"`
	URL           string          `json:"url"`
	RepositoryURL string          `json:"repository_url"`
	Title         string          `json:"title"`
	State         string          `json:"state"`
	UpdatedAt     string          `json:"updated_at"`
	PullRequest   json.RawMessage `json:"pull_request"`
	User          struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
}

func ParseIssue(raw json.RawMessage) (id string, number int64, isPR bool, err error) {
	var f issueFields
	if err = json.Unmarshal(raw, &f); err != nil {
		return "", 0, false, fmt.Errorf("decode issue object: %w", err)
	}
	if !numericID.MatchString(f.ID.String()) || f.Number <= 0 {
		return "", 0, false, fmt.Errorf("issue missing valid id or number")
	}
	return f.ID.String(), f.Number, len(f.PullRequest) > 0 && !bytes.Equal(f.PullRequest, []byte("null")), nil
}

type checkpoint struct {
	BaseCommit    string    `json:"base_commit"`
	StartedAt     time.Time `json:"started_at"`
	Full          bool      `json:"full"`
	IssuesSince   string    `json:"issues_since"`
	CommentsSince string    `json:"comments_since"`
}

func (e *Engine) Sync(ctx context.Context, request Request, reporter Reporter) (err error) {
	repo := request.Repo
	archivePath, err := issuearchive.Path(e.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		return err
	}
	gateValue, _ := repoLocks.LoadOrStore(archivePath, make(chan struct{}, 1))
	gate := gateValue.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-gate }()
	release, err := issuearchive.AcquireLock(archivePath + ".sync.lock")
	if err != nil {
		return err
	}
	defer release()
	if e.DB == nil {
		return fmt.Errorf("issue database is required")
	}
	queries := dbsqlc.New(e.DB)
	if err = queries.SetIssueSyncRunning(ctx, dbsqlc.SetIssueSyncRunningParams{RepoID: repo.ID, UpdatedAt: e.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			failureCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			writeErr := queries.SetIssueSyncFailed(failureCtx, dbsqlc.SetIssueSyncFailedParams{RepoID: repo.ID, LastError: err.Error(), UpdatedAt: e.Now().UTC().Format(time.RFC3339Nano)})
			if writeErr != nil {
				err = errors.Join(err, fmt.Errorf("record issue sync failure: %w", writeErr))
			}
		}
	}()
	store, err := issuearchive.NewStore(archivePath)
	if err != nil {
		return err
	}
	previous, loadErr := store.Load(ctx, "")
	if loadErr != nil && !errors.Is(loadErr, issuearchive.ErrNoSnapshots) {
		return loadErr
	}
	state, stateErr := queries.GetIssueSyncState(ctx, repo.ID)
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		return stateErr
	}
	history, err := store.History(ctx, 1)
	if err != nil && !errors.Is(err, issuearchive.ErrNoSnapshots) {
		return err
	}
	baseCommit := ""
	if len(history) > 0 {
		baseCommit = history[0].Commit
	}
	// A published Git snapshot wins after a crash between ref publication and index commit.
	if baseCommit != "" && state.LatestCommit != baseCommit {
		if err = e.reindexUnlocked(ctx, repo); err != nil {
			return err
		}
		state, err = queries.GetIssueSyncState(ctx, repo.ID)
		if err != nil {
			return err
		}
		if err = queries.SetIssueSyncRunning(ctx, dbsqlc.SetIssueSyncRunningParams{RepoID: repo.ID, UpdatedAt: e.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			return err
		}
	}
	full := request.Full || baseCommit == "" || state.IssuesSince == "" || state.CommentsSince == ""
	if last, parseErr := time.Parse(time.RFC3339Nano, state.LastFullReconcileAt); parseErr != nil || e.Now().Sub(last) >= ReconcileInterval {
		full = true
	}
	cp := checkpoint{BaseCommit: baseCommit, StartedAt: e.Now().UTC(), Full: full, IssuesSince: state.IssuesSince, CommentsSince: state.CommentsSince}
	stage := filepath.Join(e.DataDir, "staging", "issues", repo.SourceID, repo.RefID)
	if err = os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	reusedCheckpoint := false
	if data, readErr := os.ReadFile(filepath.Join(stage, "checkpoint.json")); readErr == nil {
		var old checkpoint
		if json.Unmarshal(data, &old) == nil && old.BaseCommit == baseCommit && !old.StartedAt.IsZero() && (!full || old.Full) {
			cp = old
			reusedCheckpoint = true
		}
	}
	if !reusedCheckpoint {
		if err = os.RemoveAll(stage); err != nil {
			return err
		}
		if err = os.MkdirAll(stage, 0o700); err != nil {
			return err
		}
	}
	if err = atomicJSON(filepath.Join(stage, "checkpoint.json"), cp); err != nil {
		return err
	}
	full = cp.Full
	if err = progress(reporter, "Reading GitHub issues", map[string]any{"phase": "issues", "repo_id": repo.ID, "full": full}); err != nil {
		return err
	}
	parts := strings.Split(repo.FullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repository full name")
	}
	endpoint := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	rawRepo, err := e.Client.Object(ctx, request.Token, endpoint)
	if err != nil {
		return err
	}
	var remote struct {
		ID        json.Number `json:"id"`
		NodeID    string      `json:"node_id"`
		FullName  string      `json:"full_name"`
		HTMLURL   string      `json:"html_url"`
		URL       string      `json:"url"`
		Private   bool        `json:"private"`
		HasIssues *bool       `json:"has_issues"`
	}
	if err = json.Unmarshal(rawRepo, &remote); err != nil {
		return fmt.Errorf("decode repository: %w", err)
	}
	if remote.ID.String() != repo.RefID {
		return fmt.Errorf("repository identity changed; refusing to overwrite archive")
	}
	if remote.HasIssues != nil && !*remote.HasIssues {
		return &githubapi.Error{Status: 410}
	}
	// Use the canonical API URL after a same-origin rename redirect.
	if remote.URL != "" {
		endpoint = remote.URL
	}
	query := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}, "per_page": {"100"}}
	if !full {
		query.Set("since", overlap(cp.IssuesSince))
	}
	rawIssues, err := e.Client.Pages(ctx, request.Token, endpoint+"/issues?"+query.Encode())
	if err != nil {
		return err
	}
	candidates := map[string]issuearchive.Issue{}
	byURL := map[string]string{}
	for id, issue := range previous.Issues {
		var f issueFields
		if json.Unmarshal(issue.Raw, &f) == nil && f.URL != "" {
			byURL[f.URL] = id
		}
	}
	for _, raw := range rawIssues {
		id, number, isPR, parseErr := ParseIssue(raw)
		if parseErr != nil {
			return parseErr
		}
		if isPR {
			continue
		}
		candidates[id] = issuearchive.Issue{ID: id, Number: number, Raw: raw, Availability: "available"}
		var f issueFields
		_ = json.Unmarshal(raw, &f)
		byURL[f.URL] = id
	}
	// Comment edits have an independent cursor. Never rely solely on issue.updated_at.
	commentQuery := url.Values{"sort": {"created"}, "direction": {"asc"}, "per_page": {"100"}}
	commentSince := cp.CommentsSince
	if full {
		commentSince = cp.StartedAt.Format(time.RFC3339Nano)
	}
	if commentSince != "" {
		commentQuery.Set("since", overlap(commentSince))
	}
	changedComments, err := e.Client.Pages(ctx, request.Token, endpoint+"/issues/comments?"+commentQuery.Encode())
	if err != nil {
		return err
	}
	commentChanged := map[string]bool{}
	for _, raw := range changedComments {
		var comment struct {
			IssueURL string `json:"issue_url"`
		}
		if json.Unmarshal(raw, &comment) != nil || comment.IssueURL == "" {
			return fmt.Errorf("comment missing issue_url")
		}
		id := byURL[comment.IssueURL]
		if id == "" || candidates[id].ID == "" {
			issueRaw, fetchErr := e.Client.Object(ctx, request.Token, comment.IssueURL)
			if fetchErr != nil {
				return fetchErr
			}
			issueID, number, isPR, parseErr := ParseIssue(issueRaw)
			if parseErr != nil {
				return parseErr
			}
			if isPR {
				continue
			}
			var f issueFields
			_ = json.Unmarshal(issueRaw, &f)
			if f.RepositoryURL != "" && remote.URL != "" && f.RepositoryURL != remote.URL {
				continue
			}
			id = issueID
			candidates[id] = issuearchive.Issue{ID: id, Number: number, Raw: issueRaw, Availability: "available"}
			byURL[comment.IssueURL] = id
		}
		commentChanged[id] = true
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for index, id := range ids {
		issue := candidates[id]
		cacheFile := filepath.Join(stage, "issue-"+id+".json")
		cachedIssue := issuearchive.Issue{}
		if data, readErr := os.ReadFile(cacheFile); readErr == nil {
			_ = json.Unmarshal(data, &cachedIssue)
		}
		// Cache only this interrupted observation window; comment changes force a refresh.
		if cachedIssue.ID == id && bytes.Equal(compact(cachedIssue.Raw), compact(issue.Raw)) && !commentChanged[id] && cachedIssue.Comments != nil && cachedIssue.Timeline != nil {
			issue.Comments = cachedIssue.Comments
			issue.Timeline = cachedIssue.Timeline
		} else {
			issuePath := endpoint + "/issues/" + strconv.FormatInt(issue.Number, 10)
			issue.Comments, err = e.Client.Pages(ctx, request.Token, issuePath+"/comments?per_page=100")
			if err != nil {
				return err
			}
			issue.Timeline, err = e.Client.Pages(ctx, request.Token, issuePath+"/timeline?per_page=100")
			if err != nil {
				return err
			}
			if err = atomicJSON(cacheFile, issue); err != nil {
				return err
			}
		}
		candidates[id] = issue
		if err = progress(reporter, fmt.Sprintf("Archived issue threads (%d/%d)", index+1, len(ids)), map[string]any{"phase": "threads", "processed": index + 1, "total": len(ids), "repo_id": repo.ID}); err != nil {
			return err
		}
	}
	labels, err := e.Client.Pages(ctx, request.Token, endpoint+"/labels?per_page=100")
	if err != nil {
		return err
	}
	milestones, err := e.Client.Pages(ctx, request.Token, endpoint+"/milestones?state=all&per_page=100")
	if err != nil {
		return err
	}
	watermark := cp.StartedAt.UTC().Format(time.RFC3339Nano)
	lastFull := state.LastFullReconcileAt
	if full {
		lastFull = watermark
	}
	snapshot := issuearchive.Snapshot{Repository: issuearchive.Repository{SourceID: repo.SourceID, ID: repo.RefID, NodeID: remote.NodeID, FullName: remote.FullName, HTMLURL: remote.HTMLURL, Private: remote.Private}, Issues: candidates, Labels: labels, Milestones: milestones, APIVersion: githubapi.Version, StartedAt: cp.StartedAt, CompletedAt: e.Now().UTC(), IssuesSince: watermark, CommentsSince: watermark, LastFullReconcileAt: lastFull, Full: full}
	saved, err := store.Save(ctx, snapshot)
	if err != nil {
		return err
	}
	merged, err := store.Load(ctx, saved.Commit)
	if err != nil {
		return err
	}
	// No-op snapshots retain their old manifest. Advance only the live safe cursor.
	merged.IssuesSince = watermark
	merged.CommentsSince = watermark
	merged.LastFullReconcileAt = lastFull
	merged.CompletedAt = snapshot.CompletedAt
	if err = e.IndexSnapshot(ctx, repo, merged, saved.Commit); err != nil {
		return err
	}
	if err = os.RemoveAll(stage); err != nil {
		return fmt.Errorf("remove completed checkpoint: %w", err)
	}
	return progress(reporter, "Issues backup complete (attachments are links only)", map[string]any{"phase": "done", "repo_id": repo.ID, "snapshot": saved.Commit, "changed": saved.Changed, "issues": saved.IssueCount, "comments": saved.CommentCount, "attachments": "links-only"})
}
func overlap(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Add(-Overlap).UTC().Format(time.RFC3339)
}
func compact(raw []byte) []byte {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return raw
	}
	return b.Bytes()
}
func progress(r Reporter, s string, m map[string]any) error {
	if r == nil {
		return nil
	}
	return r.SetProgress(s, m)
}
func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
