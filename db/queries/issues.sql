-- name: GetIssueSyncState :one
SELECT * FROM issue_sync_state WHERE repo_id = ?1;

-- name: SetIssueSyncRunning :exec
INSERT INTO issue_sync_state (repo_id, status, last_error, updated_at)
VALUES (?1, 'running', '', ?2)
ON CONFLICT (repo_id) DO UPDATE SET status = 'running', last_error = '', updated_at = excluded.updated_at;

-- name: SetIssueSyncFailed :exec
INSERT INTO issue_sync_state (repo_id, status, last_error, updated_at)
VALUES (?1, 'failed', ?2, ?3)
ON CONFLICT (repo_id) DO UPDATE SET status = 'failed', last_error = excluded.last_error, updated_at = excluded.updated_at;

-- name: CompleteIssueSync :exec
INSERT INTO issue_sync_state (repo_id, status, latest_commit, issues_since, comments_since,
 last_successful_at, last_full_reconcile_at, last_error, issue_count, comment_count, updated_at)
VALUES (?1, 'idle', ?2, ?3, ?4, ?5, ?6, '', ?7, ?8, ?5)
ON CONFLICT (repo_id) DO UPDATE SET status = 'idle', latest_commit = excluded.latest_commit,
 issues_since = excluded.issues_since, comments_since = excluded.comments_since,
 last_successful_at = excluded.last_successful_at, last_full_reconcile_at = excluded.last_full_reconcile_at,
 last_error = '', issue_count = excluded.issue_count, comment_count = excluded.comment_count,
 updated_at = excluded.updated_at;

-- name: DeleteIssueIndex :exec
DELETE FROM issues_current WHERE repo_id = ?1;

-- name: InsertIssueIndex :exec
INSERT INTO issues_current (repo_id, issue_id, number, title, state, author_login, updated_at, availability, labels_json, assignees_json)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10);

-- name: ListIssuesCurrent :many
SELECT * FROM issues_current
WHERE repo_id = ?1
  AND (?2 = '' OR state = ?2)
  AND (?3 = '' OR title LIKE '%' || ?3 || '%' OR CAST(number AS TEXT) = ?3)
ORDER BY number DESC, issue_id
LIMIT ?4 OFFSET ?5;

-- name: CountIssuesCurrent :one
SELECT COUNT(*) FROM issues_current
WHERE repo_id = ?1
  AND (?2 = '' OR state = ?2)
  AND (?3 = '' OR title LIKE '%' || ?3 || '%' OR CAST(number AS TEXT) = ?3);

-- name: ListInterruptedIssueSyncs :many
SELECT repo_id FROM issue_sync_state WHERE status = 'running' ORDER BY repo_id;
