import { sql } from 'drizzle-orm';
import {
  check,
  index,
  integer,
  sqliteTable,
  text,
  uniqueIndex,
} from 'drizzle-orm/sqlite-core';

export const appMeta = sqliteTable('app_meta', {
  key: text('key').primaryKey(),
  value: text('value').notNull(),
});

export const repos = sqliteTable(
  'repos',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    sourceId: text('source_id').notNull(),
    platform: text('platform').notNull(),
    refId: text('ref_id').notNull(),
    status: text('status').notNull().default('active'),
    name: text('name').notNull(),
    fullName: text('full_name').notNull(),
    owner: text('owner').notNull(),
    description: text('description'),
    htmlUrl: text('html_url'),
    cloneUrl: text('clone_url'),
    sshUrl: text('ssh_url'),
    defaultBranch: text('default_branch'),
    visibility: text('visibility'),
    isPrivate: integer('is_private').notNull().default(0),
    isFork: integer('is_fork').notNull().default(0),
    isArchived: integer('is_archived').notNull().default(0),
    origin: text('origin').notNull().default('{}'),
    meta: text('meta').notNull(),
    archiveRepoSizeBytes: integer('archive_repo_size_bytes'),
    lastSeenAt: text('last_seen_at').notNull(),
    disabledAt: text('disabled_at'),
    createdAt: text('created_at')
      .notNull()
      .default(sql`CURRENT_TIMESTAMP`),
    updatedAt: text('updated_at')
      .notNull()
      .default(sql`CURRENT_TIMESTAMP`),
  },
  (table) => [
    uniqueIndex('repos_source_ref_unique').on(table.sourceId, table.refId),
    index('repos_source_status_idx').on(table.sourceId, table.status),
    index('repos_source_full_name_idx').on(table.sourceId, table.fullName),
    check(
      'repos_status_check',
      sql`${table.status} IN ('active', 'auto_excluded')`,
    ),
    check('repos_origin_json_check', sql`json_valid(${table.origin})`),
    check('repos_meta_json_check', sql`json_valid(${table.meta})`),
  ],
);

export const repoRefsCurrent = sqliteTable(
  'repo_refs_current',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    repoId: integer('repo_id')
      .notNull()
      .references(() => repos.id, { onDelete: 'cascade' }),
    refName: text('ref_name').notNull(),
    refKind: text('ref_kind').notNull(),
    currentHash: text('current_hash').notNull(),
    status: text('status').notNull(),
    archiveRefName: text('archive_ref_name'),
    firstSeenAt: text('first_seen_at').notNull(),
    lastSeenAt: text('last_seen_at').notNull(),
    lastHashUpdatedAt: text('last_hash_updated_at').notNull(),
    currentCommitAuthoredAt: text('current_commit_authored_at'),
    currentCommitCommittedAt: text('current_commit_committed_at'),
    currentCommitAuthorName: text('current_commit_author_name'),
    currentCommitAuthorEmail: text('current_commit_author_email'),
    currentCommitMessage: text('current_commit_message'),
    deletedAt: text('deleted_at'),
    createdAt: text('created_at')
      .notNull()
      .default(sql`CURRENT_TIMESTAMP`),
    updatedAt: text('updated_at')
      .notNull()
      .default(sql`CURRENT_TIMESTAMP`),
  },
  (table) => [
    uniqueIndex('repo_refs_current_repo_ref_unique').on(
      table.repoId,
      table.refName,
    ),
    index('repo_refs_current_repo_status_idx').on(table.repoId, table.status),
    index('repo_refs_current_repo_kind_idx').on(table.repoId, table.refKind),
    check(
      'repo_refs_current_kind_check',
      sql`${table.refKind} IN ('head', 'tag')`,
    ),
    check(
      'repo_refs_current_status_check',
      sql`${table.status} IN ('active', 'deleted')`,
    ),
  ],
);

export const taskRuns = sqliteTable(
  'task_runs',
  {
    taskId: text('task_id').primaryKey(),
    parentTaskId: text('parent_task_id'),
    jobId: text('job_id').notNull(),
    jobType: text('job_type').notNull(),
    name: text('name').notNull(),
    argsJson: text('args_json'),
    status: text('status').notNull(),
    createdAt: text('created_at').notNull(),
    startedAt: text('started_at').notNull(),
    finishedAt: text('finished_at'),
    errorMessage: text('error_message'),
    lastProgressSummary: text('last_progress_summary'),
    lastProgressMetaJson: text('last_progress_meta_json'),
    updatedAt: text('updated_at').notNull(),
  },
  (table) => [
    index('task_runs_started_at_idx').on(table.startedAt),
    index('task_runs_job_type_idx').on(table.jobType),
    index('task_runs_parent_task_id_idx').on(table.parentTaskId),
  ],
);

export const repoRefChanges = sqliteTable(
  'repo_ref_changes',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    repoId: integer('repo_id')
      .notNull()
      .references(() => repos.id, { onDelete: 'cascade' }),
    taskRunId: text('task_run_id')
      .notNull()
      .references(() => taskRuns.taskId, { onDelete: 'cascade' }),
    refName: text('ref_name').notNull(),
    refKind: text('ref_kind').notNull(),
    action: text('action').notNull(),
    oldHash: text('old_hash'),
    newHash: text('new_hash'),
    newCommitAuthoredAt: text('new_commit_authored_at'),
    newCommitCommittedAt: text('new_commit_committed_at'),
    newCommitAuthorName: text('new_commit_author_name'),
    newCommitAuthorEmail: text('new_commit_author_email'),
    newCommitMessage: text('new_commit_message'),
    archiveRefName: text('archive_ref_name'),
    createdAt: text('created_at').notNull(),
  },
  (table) => [
    uniqueIndex('repo_ref_changes_repo_run_ref_unique').on(
      table.repoId,
      table.taskRunId,
      table.refName,
    ),
    index('repo_ref_changes_repo_created_at_idx').on(
      table.repoId,
      table.createdAt,
    ),
    index('repo_ref_changes_task_run_id_idx').on(table.taskRunId),
    index('repo_ref_changes_repo_ref_created_at_idx').on(
      table.repoId,
      table.refName,
      table.createdAt,
    ),
    check(
      'repo_ref_changes_kind_check',
      sql`${table.refKind} IN ('head', 'tag')`,
    ),
    check(
      'repo_ref_changes_action_check',
      sql`${table.action} IN ('create', 'update', 'delete')`,
    ),
  ],
);

export const taskRunLogs = sqliteTable(
  'task_run_logs',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    taskId: text('task_id')
      .notNull()
      .references(() => taskRuns.taskId, { onDelete: 'cascade' }),
    eventType: text('event_type').notNull(),
    summary: text('summary'),
    metaJson: text('meta_json'),
    errorMessage: text('error_message'),
    createdAt: text('created_at').notNull(),
  },
  (table) => [
    index('task_run_logs_task_id_created_at_idx').on(
      table.taskId,
      table.createdAt,
      table.id,
    ),
  ],
);

// The Git archive is authoritative; these rows are rebuildable browser indexes.
export const issueSyncState = sqliteTable('issue_sync_state', {
  repoId: integer('repo_id')
    .primaryKey()
    .references(() => repos.id, { onDelete: 'cascade' }),
  status: text('status').notNull().default('idle'),
  latestCommit: text('latest_commit').notNull().default(''),
  issuesSince: text('issues_since').notNull().default(''),
  commentsSince: text('comments_since').notNull().default(''),
  lastSuccessfulAt: text('last_successful_at').notNull().default(''),
  lastFullReconcileAt: text('last_full_reconcile_at').notNull().default(''),
  lastError: text('last_error').notNull().default(''),
  issueCount: integer('issue_count').notNull().default(0),
  commentCount: integer('comment_count').notNull().default(0),
  updatedAt: text('updated_at').notNull(),
});

export const issuesCurrent = sqliteTable(
  'issues_current',
  {
    repoId: integer('repo_id')
      .notNull()
      .references(() => repos.id, { onDelete: 'cascade' }),
    issueId: text('issue_id').notNull(),
    number: integer('number').notNull(),
    title: text('title').notNull(),
    state: text('state').notNull(),
    authorLogin: text('author_login').notNull(),
    updatedAt: text('updated_at').notNull(),
    availability: text('availability').notNull(),
    labelsJson: text('labels_json').notNull().default('[]'),
    assigneesJson: text('assignees_json').notNull().default('[]'),
  },
  (table) => [
    uniqueIndex('issues_current_repo_issue_unique').on(
      table.repoId,
      table.issueId,
    ),
    index('issues_current_repo_number_idx').on(table.repoId, table.number),
    index('issues_current_repo_state_idx').on(table.repoId, table.state),
  ],
);
