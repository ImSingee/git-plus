package taskservice

import (
	"context"
	"testing"
	"time"

	appdb "github.com/ImSingee/git-plus/db"
	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	"github.com/ImSingee/git-plus/pkg/issuetask"
	"github.com/ImSingee/git-plus/pkg/syncsource"
	"github.com/ImSingee/git-plus/pkg/task"
)

type noopSourceExecutor struct{}

func (noopSourceExecutor) Sync(context.Context, syncsource.SyncRequest, SourceSyncReporter) error {
	return nil
}
func TestSourceSyncQueuesOptedInIssueChildTasks(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(appconfig.TokenPassphraseEnvVar, serviceTestPassphrase)
			ctx := context.Background()
			dir := t.TempDir()
			if err := appdb.Migrate(ctx, dir); err != nil {
				t.Fatal(err)
			}
			db, err := appdb.Open(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			token, err := appconfig.EncryptToken("synthetic-token", serviceTestPassphrase)
			if err != nil {
				t.Fatal(err)
			}
			if err := appconfig.Save(appconfig.PathForDataDir(dir), appconfig.Config{Sources: []appconfig.SourceConfig{{ID: "source", Platform: "github", Username: "owner", Token: token, IssuesEnabled: enabled}}}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Format(time.RFC3339)
			if err := dbsqlc.New(db).CreateRepo(ctx, dbsqlc.CreateRepoParams{SourceID: "source", Platform: "github", RefID: "123", Status: "active", Name: "repo", FullName: "owner/repo", Owner: "owner", Origin: `["default"]`, Meta: "{}", LastSeenAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			child := make(chan task.Snapshot, 1)
			finished := make(chan task.Event, 1)
			var manager *task.Manager
			manager = task.NewManager(task.WithObserver(func(event task.Event) {
				if event.Name == task.EventTaskEnqueued && event.Task.JobType == issuetask.JobType {
					// Cancel while the source parent is still running. No GitHub request is made.
					if _, err := manager.CancelQueuedTask(event.Task.TaskID); err != nil {
						t.Error(err)
					}
					child <- event.Task
				}
				if event.Task.JobID == "test-source" && (event.Name == task.EventTaskFinished || event.Name == task.EventTaskFailed) {
					finished <- event
				}
			}))
			defer manager.Close()
			service := newServiceServer(dir, manager, WithDatabase(db), WithSourceSyncExecutor(noopSourceExecutor{}))
			_, parent, err := manager.Enqueue(task.Spec{JobID: "test-source", JobType: task.JobTypeSyncSource, Name: "Source sync", Run: service.sourceSyncRunner("source")})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-finished:
				if event.Name != task.EventTaskFinished {
					t.Fatalf("source failed: %s", event.Task.ErrorMessage)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("source did not finish")
			}
			if enabled {
				select {
				case item := <-child:
					if item.ParentTaskID != parent.TaskID || item.Args["source_id"] != "source" {
						t.Fatalf("bad child: %+v", item)
					}
				default:
					t.Fatal("opted-in source did not queue issue child")
				}
			} else {
				select {
				case <-child:
					t.Fatal("disabled source queued issues")
				default:
				}
			}
		})
	}
}
