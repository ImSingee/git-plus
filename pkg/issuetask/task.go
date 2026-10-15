// Package issuetask integrates issue archives with the existing task queue.
package issuetask

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	appconfig "github.com/ImSingee/git-plus/pkg/config"
	"github.com/ImSingee/git-plus/pkg/issuesync"
	"github.com/ImSingee/git-plus/pkg/task"
)

const JobType = "sync-issues"

var ErrDisabled = errors.New("issue backups are disabled for this source")

type Runner struct {
	DataDir string
	DB      *sql.DB
	Manager *task.Manager
	Engine  *issuesync.Engine
}

func New(dataDir string, db *sql.DB, manager *task.Manager) *Runner {
	return &Runner{DataDir: dataDir, DB: db, Manager: manager, Engine: issuesync.New(dataDir, db)}
}
func (r *Runner) Enqueue(ctx context.Context, repoID int64, full bool, parent string) (task.EnqueueResult, task.Snapshot, error) {
	if r.DB == nil || r.Manager == nil {
		return 0, task.Snapshot{}, fmt.Errorf("issue task database and manager are required")
	}
	repo, err := dbsqlc.New(r.DB).GetRepoById(ctx, repoID)
	if err != nil {
		return 0, task.Snapshot{}, err
	}
	enabled, err := IsEnabled(r.DataDir, repo.SourceID)
	if err != nil {
		return 0, task.Snapshot{}, err
	}
	if !enabled {
		return 0, task.Snapshot{}, ErrDisabled
	}
	if repo.Status != "active" {
		return 0, task.Snapshot{}, fmt.Errorf("repository is not active")
	}
	jobID := fmt.Sprintf("sync-issues::%d", repoID)
	if full {
		jobID += "::full"
	}
	return r.Manager.Enqueue(task.Spec{
		ParentTaskID: parent, JobID: jobID, JobType: JobType, Name: "Back up issues: " + repo.FullName,
		Args: map[string]any{"repo_id": repoID, "source_id": repo.SourceID, "full": full},
		Run: func(progress *task.ExecutionContext) (runErr error) {
			defer func() {
				if runErr != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := dbsqlc.New(r.DB).SetIssueSyncFailed(ctx, dbsqlc.SetIssueSyncFailedParams{RepoID: repoID, LastError: runErr.Error(), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
						runErr = errors.Join(runErr, err)
					}
				}
			}()
			// Reload source settings and credentials at execution time, never in task arguments.
			current, err := dbsqlc.New(r.DB).GetRepoById(context.Background(), repoID)
			if err != nil {
				return err
			}
			if current.Status != "active" {
				return fmt.Errorf("repository is no longer active")
			}
			loaded, err := appconfig.Load(appconfig.PathForDataDir(r.DataDir))
			if err != nil {
				return err
			}
			for _, source := range loaded.Data.Sources {
				if source.ID != current.SourceID {
					continue
				}
				if !source.IssuesEnabled {
					return ErrDisabled
				}
				token, err := appconfig.DecryptToken(source.Token, os.Getenv(appconfig.TokenPassphraseEnvVar))
				if err != nil {
					return fmt.Errorf("decrypt issue source token: %w", err)
				}
				return r.Engine.Sync(context.Background(), issuesync.Request{Repo: current, Token: token, Full: full}, progress)
			}
			return fmt.Errorf("issue source was removed")
		},
	})
}
func IsEnabled(dataDir, sourceID string) (bool, error) {
	loaded, _, err := appconfig.LoadOrDefault(appconfig.PathForDataDir(dataDir))
	if err != nil {
		return false, err
	}
	for _, source := range loaded.Data.Sources {
		if source.ID == sourceID {
			return source.IssuesEnabled, nil
		}
	}
	return false, nil
}

// ResumeInterrupted requeues only jobs marked running when the previous server exited.
func (r *Runner) ResumeInterrupted(ctx context.Context) error {
	states, err := dbsqlc.New(r.DB).ListInterruptedIssueSyncs(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range states {
		_, _, err := r.Enqueue(ctx, id, false, "")
		if err != nil {
			reason := fmt.Errorf("interrupted issue backup was not resumed: %w", err)
			if writeErr := dbsqlc.New(r.DB).SetIssueSyncFailed(ctx, dbsqlc.SetIssueSyncFailedParams{RepoID: id, LastError: reason.Error(), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); writeErr != nil {
				failures = append(failures, writeErr)
			}
			if !errors.Is(err, ErrDisabled) {
				failures = append(failures, reason)
			}
		}
	}
	return errors.Join(failures...)
}
