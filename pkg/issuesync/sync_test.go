package issuesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	"github.com/ImSingee/git-plus/pkg/githubapi"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
)

var syncTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

const fixtureRepoPath = "/repos/owner/repo"
const fixtureIssueID = "9007199254740993"

// fixtureAPI uses real HTTP and generated SQLite queries. The tests intentionally
// preserve unknown fields and >2^53 numeric IDs across API, Git and index layers.
type fixtureAPI struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []string
	issues   []int
	comments map[int][]json.RawMessage
	changed  []json.RawMessage
	failures map[string]int
	paginate bool
	remoteID string
}

func newFixture(t *testing.T) *fixtureAPI {
	t.Helper()
	f := &fixtureAPI{issues: []int{1, 2, 99}, comments: map[int][]json.RawMessage{1: {json.RawMessage(`{"id":501,"body":"original comment"}`)}}, failures: map[string]int{}, remoteID: "123"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixtureAPI) issue(number int) json.RawMessage {
	id := "2"
	if number == 1 {
		id = fixtureIssueID
	}
	if number == 99 {
		return json.RawMessage(`{"id":99,"number":99,"title":"A pull request","pull_request":{"url":"ignored"}}`)
	}
	return json.RawMessage(fmt.Sprintf(`{"id":%s,"number":%d,"title":"Issue %d","body":"Body","state":"open","url":%q,"repository_url":%q,"html_url":"https://github.com/owner/repo/issues/%d","updated_at":"2026-10-15T11:00:00Z","unknown":{"large":18446744073709551616001},"user":{"login":"author"},"labels":[{"name":"bug"}],"assignees":[{"login":"reviewer"}]}`, id, number, number, f.server.URL+fixtureRepoPath+fmt.Sprintf("/issues/%d", number), f.server.URL+fixtureRepoPath, number))
}

func (f *fixtureAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.String())
	if status := f.failures[r.URL.Path]; status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, `{"message":"private-token-do-not-log"}`)
		return
	}
	if r.URL.Path == fixtureRepoPath {
		fmt.Fprintf(w, `{"id":%s,"node_id":"R_123","full_name":"owner/repo","url":%q,"html_url":"https://github.com/owner/repo","private":true,"has_issues":true}`, f.remoteID, f.server.URL+fixtureRepoPath)
		return
	}
	page := func(items []json.RawMessage) {
		if items == nil {
			items = []json.RawMessage{}
		}
		if f.paginate && len(items) > 1 {
			if r.URL.Query().Get("page") != "2" {
				query := r.URL.Query()
				query.Set("page", "2")
				w.Header().Set("Link", fmt.Sprintf(`<%s%s?%s>; rel="next"`, f.server.URL, r.URL.Path, query.Encode()))
				items = items[:1]
			} else {
				items = items[1:]
			}
		}
		_ = json.NewEncoder(w).Encode(items)
	}
	switch r.URL.Path {
	case fixtureRepoPath + "/issues":
		items := make([]json.RawMessage, 0, len(f.issues))
		for _, number := range f.issues {
			items = append(items, f.issue(number))
		}
		page(items)
	case fixtureRepoPath + "/issues/comments":
		page(f.changed)
	case fixtureRepoPath + "/issues/1":
		w.Write(f.issue(1))
	case fixtureRepoPath + "/issues/2":
		w.Write(f.issue(2))
	case fixtureRepoPath + "/issues/99":
		w.Write(f.issue(99))
	case fixtureRepoPath + "/issues/1/comments":
		page(f.comments[1])
	case fixtureRepoPath + "/issues/2/comments":
		page(f.comments[2])
	case fixtureRepoPath + "/issues/1/timeline", fixtureRepoPath + "/issues/2/timeline":
		page([]json.RawMessage{json.RawMessage(`{"id":11,"event":"labeled"}`), json.RawMessage(`{"id":12,"event":"assigned"}`)})
	case fixtureRepoPath + "/labels":
		page([]json.RawMessage{json.RawMessage(`{"id":1,"name":"bug"}`), json.RawMessage(`{"id":2,"name":"help wanted"}`)})
	case fixtureRepoPath + "/milestones":
		page([]json.RawMessage{json.RawMessage(`{"id":1,"title":"v1","state":"open"}`), json.RawMessage(`{"id":2,"title":"old","state":"closed"}`)})
	default:
		w.WriteHeader(404)
	}
}

func (f *fixtureAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if strings.Split(request, "?")[0] == path {
			count++
		}
	}
	return count
}

func syncTestDB(t *testing.T) (*sql.DB, dbsqlc.Repo) {
	t.Helper()
	dir := t.TempDir()
	if err := appdb.Migrate(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	db, err := appdb.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	q := dbsqlc.New(db)
	if err := q.CreateRepo(context.Background(), dbsqlc.CreateRepoParams{SourceID: "source-1", Platform: "github", RefID: "123", Status: "active", Name: "repo", FullName: "owner/repo", Owner: "owner", Origin: "{}", Meta: "{}", LastSeenAt: syncTime.Format(time.RFC3339), CreatedAt: syncTime.Format(time.RFC3339), UpdatedAt: syncTime.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	repo, err := q.GetRepoById(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return db, repo
}

func syncHarness(t *testing.T) (*Engine, *fixtureAPI, dbsqlc.Repo) {
	t.Helper()
	db, repo := syncTestDB(t)
	api := newFixture(t)
	engine := New(t.TempDir(), db)
	engine.Client = githubapi.New(api.server.URL, api.server.Client())
	engine.Client.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	engine.Now = func() time.Time { return syncTime }
	return engine, api, repo
}

func runSync(t *testing.T, e *Engine, repo dbsqlc.Repo, full bool) {
	t.Helper()
	if err := e.Sync(context.Background(), Request{Repo: repo, Token: "private-token-do-not-log", Full: full}, nil); err != nil {
		t.Fatal(err)
	}
}

func syncState(t *testing.T, e *Engine, repo dbsqlc.Repo) dbsqlc.IssueSyncState {
	t.Helper()
	state, err := dbsqlc.New(e.DB).GetIssueSyncState(context.Background(), repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func syncStore(t *testing.T, e *Engine, repo dbsqlc.Repo) *issuearchive.Store {
	t.Helper()
	path, err := issuearchive.Path(e.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := issuearchive.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFullSyncPaginatesAllCollectionsAndSkipsPullRequests(t *testing.T) {
	e, api, repo := syncHarness(t)
	api.paginate = true
	api.comments[1] = append(api.comments[1], json.RawMessage(`{"id":502,"body":"page two"}`))
	runSync(t, e, repo, false)
	state := syncState(t, e, repo)
	if state.Status != "idle" || state.IssueCount != 2 || state.CommentCount != 2 || state.LatestCommit == "" || state.IssuesSince != syncTime.Format(time.RFC3339) || state.CommentsSince != state.IssuesSince {
		t.Fatalf("unexpected state: %+v", state)
	}
	store := syncStore(t, e, repo)
	snapshot, err := store.Load(context.Background(), state.LatestCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Issues) != 2 || len(snapshot.Labels) != 2 || len(snapshot.Milestones) != 2 || len(snapshot.Issues[fixtureIssueID].Timeline) != 2 {
		t.Fatalf("pagination data missing: %+v", snapshot)
	}
	if !strings.Contains(string(snapshot.Issues[fixtureIssueID].Raw), "18446744073709551616001") {
		t.Fatal("large numeric unknown field lost")
	}
	for _, path := range []string{"/issues", "/issues/1/comments", "/issues/1/timeline", "/labels", "/milestones"} {
		if api.count(fixtureRepoPath+path) != 2 {
			t.Errorf("collection not fully paginated: %s", path)
		}
	}
	if api.count(fixtureRepoPath+"/issues/99/comments") != 0 {
		t.Fatal("fetched PR comments")
	}
	rows, err := dbsqlc.New(e.DB).ListIssuesCurrent(context.Background(), dbsqlc.ListIssuesCurrentParams{RepoID: repo.ID, Column2: "", Column3: "", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].IssueID != fixtureIssueID || rows[1].LabelsJson != `["bug"]` || rows[1].AssigneesJson != `["reviewer"]` {
		t.Fatalf("index incorrect: %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(e.DataDir, "staging", "issues", repo.SourceID, repo.RefID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful checkpoint not removed: %v", err)
	}
}

func TestIndependentCommentCursorCapturesEditAndNoOpAdvancesLiveCursor(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	next := syncTime.Add(time.Hour)
	e.Now = func() time.Time { return next }
	api.issues = nil // The issue.updated_at feed did not change.
	api.comments[1] = []json.RawMessage{json.RawMessage(`{"id":501,"body":"edited independently"}`)}
	api.changed = []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"id":501,"issue_url":%q}`, api.server.URL+fixtureRepoPath+"/issues/1"))}
	runSync(t, e, repo, false)
	after := syncState(t, e, repo)
	if after.LatestCommit == before.LatestCommit || after.IssueCount != 2 || after.CommentsSince != next.Format(time.RFC3339) {
		t.Fatalf("independent comment edit not saved: %+v", after)
	}
	snapshot, err := syncStore(t, e, repo).Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot.Issues[fixtureIssueID].Comments[0]), "edited independently") {
		t.Fatal("comment edit lost")
	}
	if api.count(fixtureRepoPath+"/issues/1") != 1 {
		t.Fatal("comment-only change did not fetch issue")
	}
	api.changed = nil
	next = next.Add(time.Hour)
	runSync(t, e, repo, false)
	noop := syncState(t, e, repo)
	if noop.LatestCommit != after.LatestCommit || noop.IssuesSince != next.Format(time.RFC3339) || noop.CommentsSince != next.Format(time.RFC3339) {
		t.Fatalf("no-op live cursor incorrect: %+v", noop)
	}
	history, err := syncStore(t, e, repo).History(context.Background(), 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("no-op created a snapshot: %+v %v", history, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	foundIssue, foundComment := false, false
	for _, request := range api.requests {
		if strings.Contains(request, "since=2026-10-15T11%3A55%3A00Z") {
			if strings.HasPrefix(request, fixtureRepoPath+"/issues?") {
				foundIssue = true
			}
			if strings.HasPrefix(request, fixtureRepoPath+"/issues/comments?") {
				foundComment = true
			}
		}
	}
	if !foundIssue || !foundComment {
		t.Fatal("independent overlapping cursors missing")
	}
}

func TestFailedSyncDoesNotAdvanceWatermarksOrPublishPartialSnapshot(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	e.Now = func() time.Time { return syncTime.Add(time.Hour) }
	api.failures[fixtureRepoPath+"/issues/1/timeline"] = 403
	if err := e.Sync(context.Background(), Request{Repo: repo, Token: "private-token-do-not-log"}, nil); err == nil {
		t.Fatal("expected permission failure")
	}
	after := syncState(t, e, repo)
	if after.Status != "failed" || after.LatestCommit != before.LatestCommit || after.IssuesSince != before.IssuesSince || after.CommentsSince != before.CommentsSince || after.LastSuccessfulAt != before.LastSuccessfulAt {
		t.Fatalf("failure advanced state: before=%+v after=%+v", before, after)
	}
	if strings.Contains(after.LastError, "private-token-do-not-log") {
		t.Fatal("failure leaked token or API body into SQLite")
	}
	history, err := syncStore(t, e, repo).History(context.Background(), 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("partial snapshot published: %+v %v", history, err)
	}
}

func TestInaccessibleRepositoryRetainsArchiveAndIndex(t *testing.T) {
	for _, status := range []int{403, 404, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e, api, repo := syncHarness(t)
			runSync(t, e, repo, false)
			before := syncState(t, e, repo)
			api.failures[fixtureRepoPath] = status
			if err := e.Sync(context.Background(), Request{Repo: repo, Full: true}, nil); err == nil {
				t.Fatal("expected inaccessible repository failure")
			}
			after := syncState(t, e, repo)
			if after.LatestCommit != before.LatestCommit || after.IssueCount != before.IssueCount || after.IssuesSince != before.IssuesSince {
				t.Fatal("inaccessible repo replaced a good backup")
			}
			snapshot, err := syncStore(t, e, repo).Load(context.Background(), "")
			if err != nil || len(snapshot.Issues) != 2 || snapshot.Issues[fixtureIssueID].Availability != issuearchive.AvailabilityAvailable {
				t.Fatalf("retained snapshot changed: %+v %v", snapshot, err)
			}
		})
	}
}

func TestFullReconcileRetainsMissingIssueAsUnavailable(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	api.issues = []int{2}
	e.Now = func() time.Time { return syncTime.Add(24 * time.Hour) }
	runSync(t, e, repo, true)
	snapshot, err := syncStore(t, e, repo).Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	missing := snapshot.Issues[fixtureIssueID]
	if missing.Availability != issuearchive.AvailabilityUnavailable || missing.MissingSince == "" || len(missing.Comments) != 1 || len(missing.Raw) == 0 {
		t.Fatalf("missing issue was not safely retained: %+v", missing)
	}
	if snapshot.Issues["2"].Availability != issuearchive.AvailabilityAvailable {
		t.Fatal("present issue was marked unavailable")
	}
	rows, err := dbsqlc.New(e.DB).ListIssuesCurrent(context.Background(), dbsqlc.ListIssuesCurrentParams{RepoID: repo.ID, Column2: "", Column3: "", Limit: 100})
	if err != nil || len(rows) != 2 || rows[1].Availability != issuearchive.AvailabilityUnavailable {
		t.Fatalf("index did not retain missing issue: %+v %v", rows, err)
	}
}

func TestInterruptedCheckpointResumesCompletedIssueThreads(t *testing.T) {
	e, api, repo := syncHarness(t)
	// IDs are processed lexicographically: issue 2 completes before the large ID.
	api.failures[fixtureRepoPath+"/issues/1/timeline"] = 503
	if err := e.Sync(context.Background(), Request{Repo: repo}, nil); err == nil {
		t.Fatal("expected interrupted first snapshot")
	}
	stage := filepath.Join(e.DataDir, "staging", "issues", repo.SourceID, repo.RefID)
	if _, err := os.Stat(filepath.Join(stage, "issue-2.json")); err != nil {
		t.Fatalf("completed issue checkpoint missing: %v", err)
	}
	if _, err := syncStore(t, e, repo).Load(context.Background(), ""); !errors.Is(err, issuearchive.ErrNoSnapshots) {
		t.Fatalf("failed first run published data: %v", err)
	}
	completedCalls := api.count(fixtureRepoPath + "/issues/2/comments")
	delete(api.failures, fixtureRepoPath+"/issues/1/timeline")
	e.Now = func() time.Time { return syncTime.Add(time.Hour) }
	runSync(t, e, repo, false)
	if api.count(fixtureRepoPath+"/issues/2/comments") != completedCalls {
		t.Fatal("resume re-fetched an already completed thread")
	}
	state := syncState(t, e, repo)
	if state.IssuesSince != syncTime.Format(time.RFC3339) || state.CommentsSince != syncTime.Format(time.RFC3339) {
		t.Fatalf("resume skipped original observation window: %+v", state)
	}
	if state.LastSuccessfulAt != syncTime.Add(time.Hour).Format(time.RFC3339) {
		t.Fatal("completion time not updated after resume")
	}
}

func TestOfflineReindexRebuildsFreshSQLiteAndSharesSyncLock(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	original := syncState(t, e, repo)
	beforeCalls := api.count(fixtureRepoPath)
	rebuiltDB, rebuiltRepo := syncTestDB(t)
	e.DB = rebuiltDB
	path, err := issuearchive.Path(e.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := issuearchive.AcquireLock(path + ".sync.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Reindex(context.Background(), rebuiltRepo, ""); !errors.Is(err, issuearchive.ErrBusy) {
		t.Fatalf("reindex ignored cross-process sync lock: %v", err)
	}
	if err := e.Sync(context.Background(), Request{Repo: rebuiltRepo}, nil); !errors.Is(err, issuearchive.ErrBusy) {
		t.Fatalf("sync ignored cross-process lock: %v", err)
	}
	release()
	if err := e.Reindex(context.Background(), rebuiltRepo, ""); err != nil {
		t.Fatal(err)
	}
	restored := syncState(t, e, rebuiltRepo)
	if restored.LatestCommit != original.LatestCommit || restored.IssueCount != 2 || restored.CommentCount != 1 || restored.IssuesSince != original.IssuesSince {
		t.Fatalf("reindex state incorrect: %+v", restored)
	}
	rows, err := dbsqlc.New(e.DB).ListIssuesCurrent(context.Background(), dbsqlc.ListIssuesCurrentParams{RepoID: rebuiltRepo.ID, Column2: "", Column3: "", Limit: 100})
	if err != nil || len(rows) != 2 {
		t.Fatalf("index not rebuilt: %+v %v", rows, err)
	}
	if api.count(fixtureRepoPath) != beforeCalls {
		t.Fatal("offline reindex contacted GitHub")
	}
	if err := e.Reindex(context.Background(), rebuiltRepo, original.LatestCommit); err == nil {
		t.Fatal("historical reindex allowed replacing current index")
	}
}

func TestRepositoryIdentityChangeCannotReplaceBackup(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	api.remoteID = "124"
	if err := e.Sync(context.Background(), Request{Repo: repo, Full: true}, nil); err == nil {
		t.Fatal("accepted repository identity change")
	}
	if after := syncState(t, e, repo); after.LatestCommit != before.LatestCommit {
		t.Fatal("repository identity change overwrote archive")
	}
}

func TestPublishedGitSnapshotWinsAfterIndexCommitCrash(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	store := syncStore(t, e, repo)
	snapshot, err := store.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	issue := snapshot.Issues[fixtureIssueID]
	issue.Comments = append(issue.Comments, json.RawMessage(`{"id":502,"body":"published before database crash"}`))
	snapshot.Issues[fixtureIssueID] = issue
	snapshot.CompletedAt = syncTime.Add(time.Hour)
	snapshot.IssuesSince = snapshot.CompletedAt.Format(time.RFC3339)
	snapshot.CommentsSince = snapshot.IssuesSince
	published, err := store.Save(context.Background(), snapshot)
	if err != nil || !published.Changed {
		t.Fatalf("publish simulated crash snapshot: %+v %v", published, err)
	}
	if state := syncState(t, e, repo); state.LatestCommit != before.LatestCommit {
		t.Fatal("test must leave SQLite behind Git")
	}
	api.failures[fixtureRepoPath] = 404
	e.Now = func() time.Time { return syncTime.Add(2 * time.Hour) }
	if err := e.Sync(context.Background(), Request{Repo: repo}, nil); err == nil {
		t.Fatal("expected subsequent API failure")
	}
	recovered := syncState(t, e, repo)
	if recovered.LatestCommit != published.Commit || recovered.CommentCount != 2 || recovered.IssuesSince != snapshot.IssuesSince || recovered.Status != "failed" {
		t.Fatalf("published snapshot did not win during crash recovery: %+v", recovered)
	}
}

func TestFailedIndexReplacementRollsBackEntireTransaction(t *testing.T) {
	e, _, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	snapshot, err := syncStore(t, e, repo).Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	issue := snapshot.Issues[fixtureIssueID]
	issue.Raw = json.RawMessage(`{"id":3,"number":1,"title":"identity mismatch"}`)
	snapshot.Issues[fixtureIssueID] = issue
	if err := e.IndexSnapshot(context.Background(), repo, snapshot, "unpublished"); err == nil {
		t.Fatal("invalid index replacement succeeded")
	}
	after := syncState(t, e, repo)
	if after.LatestCommit != before.LatestCommit || after.IssueCount != before.IssueCount {
		t.Fatal("failed transaction changed sync state")
	}
	rows, err := dbsqlc.New(e.DB).ListIssuesCurrent(context.Background(), dbsqlc.ListIssuesCurrentParams{RepoID: repo.ID, Column2: "", Column3: "", Limit: 100})
	if err != nil || len(rows) != 2 || rows[1].IssueID != fixtureIssueID {
		t.Fatalf("failed transaction lost prior index rows: %+v %v", rows, err)
	}
}

func TestSyncCancellationRetainsPriorSnapshot(t *testing.T) {
	e, api, repo := syncHarness(t)
	runSync(t, e, repo, false)
	before := syncState(t, e, repo)
	api.failures[fixtureRepoPath+"/issues/1/comments"] = 503
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Client.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if err := e.Sync(ctx, Request{Repo: repo}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("sync cancellation not propagated: %v", err)
	}
	after := syncState(t, e, repo)
	if after.LatestCommit != before.LatestCommit || after.IssuesSince != before.IssuesSince || after.CommentsSince != before.CommentsSince || after.Status != "failed" {
		t.Fatalf("cancelled sync changed snapshot/cursors: %+v", after)
	}
}
