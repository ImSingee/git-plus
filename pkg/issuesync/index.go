package issuesync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbsqlc "github.com/ImSingee/git-plus/db/sqlc"
	"github.com/ImSingee/git-plus/pkg/issuearchive"
)

// IndexSnapshot replaces the disposable index atomically, after Git publication.
func (e *Engine) IndexSnapshot(ctx context.Context, repo dbsqlc.Repo, snapshot issuearchive.Snapshot, commit string) error {
	if snapshot.Repository.ID != repo.RefID || snapshot.Repository.SourceID != repo.SourceID {
		return fmt.Errorf("snapshot repository identity does not match index target")
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := dbsqlc.New(tx)
	if err := q.DeleteIssueIndex(ctx, repo.ID); err != nil {
		return err
	}
	commentCount := int64(0)
	for id, issue := range snapshot.Issues {
		parsedID, number, isPR, err := ParseIssue(issue.Raw)
		if err != nil {
			return err
		}
		if parsedID != id || isPR {
			return fmt.Errorf("invalid issue identity in snapshot")
		}
		var fields issueFields
		if err := json.Unmarshal(issue.Raw, &fields); err != nil {
			return err
		}
		labels := make([]string, 0, len(fields.Labels))
		assignees := make([]string, 0, len(fields.Assignees))
		for _, label := range fields.Labels {
			labels = append(labels, label.Name)
		}
		for _, assignee := range fields.Assignees {
			assignees = append(assignees, assignee.Login)
		}
		labelsJSON, _ := json.Marshal(labels)
		assigneesJSON, _ := json.Marshal(assignees)
		if err := q.InsertIssueIndex(ctx, dbsqlc.InsertIssueIndexParams{
			RepoID: repo.ID, IssueID: id, Number: number, Title: fields.Title, State: fields.State,
			AuthorLogin: fields.User.Login, UpdatedAt: fields.UpdatedAt, Availability: issue.Availability,
			LabelsJson: string(labelsJSON), AssigneesJson: string(assigneesJSON),
		}); err != nil {
			return err
		}
		commentCount += int64(len(issue.Comments))
	}
	if err := q.CompleteIssueSync(ctx, dbsqlc.CompleteIssueSyncParams{
		RepoID: repo.ID, LatestCommit: commit, IssuesSince: snapshot.IssuesSince, CommentsSince: snapshot.CommentsSince,
		LastSuccessfulAt: snapshot.CompletedAt.UTC().Format(time.RFC3339Nano), LastFullReconcileAt: snapshot.LastFullReconcileAt,
		IssueCount: int64(len(snapshot.Issues)), CommentCount: commentCount,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Reindex restores the latest archive index. Historical snapshots never become current.
func (e *Engine) Reindex(ctx context.Context, repo dbsqlc.Repo, revision string) error {
	if revision != "" {
		return fmt.Errorf("only the latest snapshot can rebuild the current index")
	}
	path, err := issuearchive.Path(e.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		return err
	}
	release, err := issuearchive.AcquireLock(path + ".sync.lock")
	if err != nil {
		return err
	}
	defer release()
	return e.reindexUnlocked(ctx, repo)
}

func (e *Engine) reindexUnlocked(ctx context.Context, repo dbsqlc.Repo) error {
	path, err := issuearchive.Path(e.DataDir, repo.SourceID, repo.RefID)
	if err != nil {
		return err
	}
	store, err := issuearchive.NewStore(path)
	if err != nil {
		return err
	}
	if err := store.Verify(ctx, ""); err != nil {
		return err
	}
	snapshot, err := store.Load(ctx, "")
	if err != nil {
		return err
	}
	history, err := store.History(ctx, 1)
	if err != nil {
		return err
	}
	if len(history) == 0 {
		return issuearchive.ErrNoSnapshots
	}
	return e.IndexSnapshot(ctx, repo, snapshot, history[0].Commit)
}
