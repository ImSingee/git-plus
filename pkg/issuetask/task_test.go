package issuetask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	"github.com/ImSingee/git-plus/pkg/githubapi"
	"github.com/ImSingee/git-plus/pkg/task"
	"github.com/ImSingee/git-plus/pkg/taskstore"
)

type runnerFixture struct {
	runner  *Runner
	db      *sql.DB
	repo    dbsqlc.Repo
	events  chan task.Event
	headers chan string
	config  appconfig.Config
}

func newRunnerFixture(t *testing.T, enabled bool, token string) *runnerFixture {
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
	now := "2026-10-15T12:00:00Z"
	if err := q.CreateRepo(context.Background(), dbsqlc.CreateRepoParams{SourceID: "source-1", Platform: "github", RefID: "123", Status: "active", Name: "repo", FullName: "owner/repo", Owner: "owner", Origin: "{}", Meta: "{}", LastSeenAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repo, err := q.GetRepoById(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan task.Event, 100)
	manager := task.NewManager(task.WithRecorder(taskstore.NewRecorder(db)), task.WithObserver(func(event task.Event) { events <- event }))
	t.Cleanup(manager.Close)
	headers := make(chan string, 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get("Authorization")
		if r.URL.Path == "/repos/owner/repo" {
			io.WriteString(w, `{"id":123,"full_name":"owner/repo","has_issues":true}`)
			return
		}
		io.WriteString(w, `[]`)
	}))
	t.Cleanup(server.Close)
	runner := New(dir, db, manager)
	runner.Engine.Client = githubapi.New(server.URL, server.Client())
	runner.Engine.Now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	fixture := &runnerFixture{runner: runner, db: db, repo: repo, events: events, headers: headers, config: appconfig.Config{Sources: []appconfig.SourceConfig{{ID: repo.SourceID, Platform: "github", Username: "owner", IssuesEnabled: enabled, Token: token}}}}
	fixture.save(t)
	return fixture
}

func (f *runnerFixture) save(t *testing.T) {
	t.Helper()
	if err := appconfig.Save(appconfig.PathForDataDir(f.runner.DataDir), f.config); err != nil {
		t.Fatal(err)
	}
}

func (f *runnerFixture) waitTerminal(t *testing.T, taskID string) task.Event {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-f.events:
			if event.Task.TaskID == taskID && (event.Name == task.EventTaskFinished || event.Name == task.EventTaskFailed) {
				return event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for issue task")
			return task.Event{}
		}
	}
}

func (f *runnerFixture) blockQueue(t *testing.T) func() {
	t.Helper()
	blocked := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(blocked) }) }
	_, _, err := f.runner.Manager.Enqueue(task.Spec{JobID: "test-blocker", JobType: "test", Name: "Hold task queue", Run: func(*task.ExecutionContext) error { <-blocked; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

func encryptedTestToken(t *testing.T, token string) string {
	t.Helper()
	encrypted, err := appconfig.EncryptToken(token, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func TestDisabledOrMissingSourcesDoNotQueue(t *testing.T) {
	f := newRunnerFixture(t, false, "invalid-token-must-not-be-decrypted")
	if _, _, err := f.runner.Enqueue(context.Background(), f.repo.ID, false, ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled source: %v", err)
	}
	f.config.Sources = nil
	f.save(t)
	if enabled, err := IsEnabled(f.runner.DataDir, f.repo.SourceID); err != nil || enabled {
		t.Fatalf("missing source enabled=%v err=%v", enabled, err)
	}
	if _, _, err := f.runner.Enqueue(context.Background(), f.repo.ID, false, ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("missing source: %v", err)
	}
	if runtime := f.runner.Manager.Runtime(); runtime.Running != nil || len(runtime.Queued) != 0 {
		t.Fatal("disabled source queued work")
	}
	select {
	case <-f.headers:
		t.Fatal("disabled source contacted API")
	default:
	}
}

func TestCredentialsResolvedOnlyAtExecutionAndParentRecorded(t *testing.T) {
	t.Setenv(appconfig.TokenPassphraseEnvVar, "test-passphrase")
	// Deliberately malformed at enqueue: any eager decryption would fail.
	f := newRunnerFixture(t, true, "invalid-token-at-enqueue")
	release := f.blockQueue(t)
	result, item, err := f.runner.Enqueue(context.Background(), f.repo.ID, true, "parent-source-task")
	if err != nil || result != task.EnqueueResultQueued {
		t.Fatalf("enqueue should not decrypt credentials: result=%v err=%v", result, err)
	}
	if item.ParentTaskID != "parent-source-task" || item.JobType != JobType || item.JobID != fmt.Sprintf("sync-issues::%d::full", f.repo.ID) {
		t.Fatalf("queue identity incorrect: %+v", item)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "invalid-token") || strings.Contains(string(raw), "test-passphrase") || len(item.Args) != 3 {
		t.Fatalf("credentials present in task snapshot: %s", raw)
	}
	freshToken := "fresh-token-at-execution"
	f.config.Sources[0].Token = encryptedTestToken(t, freshToken)
	f.save(t)
	release()
	event := f.waitTerminal(t, item.TaskID)
	if event.Name != task.EventTaskFinished {
		t.Fatalf("task failed: %+v", event)
	}
	select {
	case authorization := <-f.headers:
		if authorization != "Bearer "+freshToken {
			t.Fatalf("used stale credential: %q", authorization)
		}
	default:
		t.Fatal("enabled task did not contact API")
	}
	stored, err := dbsqlc.New(f.db).GetTaskRun(context.Background(), item.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ParentTaskID.Valid || stored.ParentTaskID.String != "parent-source-task" || strings.Contains(stored.ArgsJson.String, freshToken) || strings.Contains(stored.ArgsJson.String, f.config.Sources[0].Token) {
		t.Fatalf("task record lost parent or leaked credential: %+v", stored)
	}
	state, err := dbsqlc.New(f.db).GetIssueSyncState(context.Background(), f.repo.ID)
	if err != nil || state.Status != "idle" || state.LatestCommit == "" {
		t.Fatalf("task failed to publish snapshot: %+v %v", state, err)
	}
}

func TestSourceDisabledWhileQueuedPreventsExecution(t *testing.T) {
	f := newRunnerFixture(t, true, "invalid-token-must-not-be-decrypted")
	release := f.blockQueue(t)
	_, item, err := f.runner.Enqueue(context.Background(), f.repo.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	f.config.Sources[0].IssuesEnabled = false
	f.save(t)
	release()
	event := f.waitTerminal(t, item.TaskID)
	if event.Name != task.EventTaskFailed || event.ErrorMessage != ErrDisabled.Error() {
		t.Fatalf("queued disable was ignored: %+v", event)
	}
	select {
	case <-f.headers:
		t.Fatal("disabled queued task contacted API")
	default:
	}
	state, err := dbsqlc.New(f.db).GetIssueSyncState(context.Background(), f.repo.ID)
	if err != nil || state.Status != "failed" {
		t.Fatalf("disabled execution status: %+v %v", state, err)
	}
}

func TestDecryptFailureDoesNotLeakToken(t *testing.T) {
	secret := "private-token-never-print"
	f := newRunnerFixture(t, true, secret)
	_, item, err := f.runner.Enqueue(context.Background(), f.repo.ID, false, "")
	if err != nil {
		t.Fatal(err)
	}
	event := f.waitTerminal(t, item.TaskID)
	if event.Name != task.EventTaskFailed || !strings.Contains(event.ErrorMessage, "decrypt issue source token") || strings.Contains(event.ErrorMessage, secret) {
		t.Fatalf("unsafe decryption failure: %+v", event)
	}
	stored, err := dbsqlc.New(f.db).GetTaskRun(context.Background(), item.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.ErrorMessage.String, secret) || strings.Contains(stored.ArgsJson.String, secret) {
		t.Fatal("token leaked into persistent task log")
	}
	select {
	case <-f.headers:
		t.Fatal("failed decryption contacted API")
	default:
	}
}

func TestResumeRequeuesOnlyRunningIssueSync(t *testing.T) {
	t.Setenv(appconfig.TokenPassphraseEnvVar, "test-passphrase")
	f := newRunnerFixture(t, true, encryptedTestToken(t, "resume-token"))
	q := dbsqlc.New(f.db)
	if err := q.SetIssueSyncRunning(context.Background(), dbsqlc.SetIssueSyncRunningParams{RepoID: f.repo.ID, UpdatedAt: "2026-10-15T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	release := f.blockQueue(t)
	if err := f.runner.ResumeInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime := f.runner.Manager.Runtime()
	if len(runtime.Queued) != 1 || runtime.Queued[0].JobType != JobType || runtime.Queued[0].Args["repo_id"] != f.repo.ID {
		t.Fatalf("running sync was not requeued: %+v", runtime)
	}
	item := runtime.Queued[0]
	release()
	if event := f.waitTerminal(t, item.TaskID); event.Name != task.EventTaskFinished {
		t.Fatalf("resumed task failed: %+v", event)
	}
	if err := f.runner.ResumeInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime = f.runner.Manager.Runtime()
	if runtime.Running != nil || len(runtime.Queued) != 0 {
		t.Fatal("completed sync was requeued on restart")
	}
}

func TestDisabledInterruptedSyncDoesNotRemainRunning(t *testing.T) {
	f := newRunnerFixture(t, false, "unused")
	q := dbsqlc.New(f.db)
	if err := q.SetIssueSyncRunning(context.Background(), dbsqlc.SetIssueSyncRunningParams{RepoID: f.repo.ID, UpdatedAt: "2026-10-15T11:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.ResumeInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := q.GetIssueSyncState(context.Background(), f.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status == "running" {
		t.Fatal("disabled interrupted sync is displayed as running forever")
	}
	if runtime := f.runner.Manager.Runtime(); runtime.Running != nil || len(runtime.Queued) != 0 {
		t.Fatal("disabled interrupted sync was queued")
	}
}

func TestInactiveRepositoryCannotQueue(t *testing.T) {
	f := newRunnerFixture(t, true, "unused")
	if err := dbsqlc.New(f.db).MarkRepoAutoExcluded(context.Background(), dbsqlc.MarkRepoAutoExcludedParams{SourceID: f.repo.SourceID, RefID: f.repo.RefID, Status: "auto_excluded", UpdatedAt: "2026-10-15T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.runner.Enqueue(context.Background(), f.repo.ID, false, ""); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("inactive repository queued: %v", err)
	}
}
