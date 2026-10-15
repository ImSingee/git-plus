CREATE TABLE app_meta (
	key text PRIMARY KEY,
	value text NOT NULL
);

CREATE TABLE issue_sync_state (
	repo_id integer PRIMARY KEY,
	status text DEFAULT 'idle' NOT NULL,
	latest_commit text DEFAULT '' NOT NULL,
	issues_since text DEFAULT '' NOT NULL,
	comments_since text DEFAULT '' NOT NULL,
	last_successful_at text DEFAULT '' NOT NULL,
	last_full_reconcile_at text DEFAULT '' NOT NULL,
	last_error text DEFAULT '' NOT NULL,
	issue_count integer DEFAULT 0 NOT NULL,
	comment_count integer DEFAULT 0 NOT NULL,
	updated_at text NOT NULL,
	CONSTRAINT fk_issue_sync_state_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
);

CREATE TABLE issues_current (
	repo_id integer NOT NULL,
	issue_id text NOT NULL,
	number integer NOT NULL,
	title text NOT NULL,
	state text NOT NULL,
	author_login text NOT NULL,
	updated_at text NOT NULL,
	availability text NOT NULL,
	labels_json text DEFAULT '[]' NOT NULL,
	assignees_json text DEFAULT '[]' NOT NULL,
	CONSTRAINT fk_issues_current_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
);

CREATE TABLE repo_ref_changes (
	id integer PRIMARY KEY AUTOINCREMENT,
	repo_id integer NOT NULL,
	task_run_id text NOT NULL,
	ref_name text NOT NULL,
	ref_kind text NOT NULL,
	action text NOT NULL,
	old_hash text,
	new_hash text,
	new_commit_authored_at text,
	new_commit_committed_at text,
	new_commit_author_name text,
	new_commit_author_email text,
	new_commit_message text,
	archive_ref_name text,
	created_at text NOT NULL,
	CONSTRAINT fk_repo_ref_changes_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE,
	CONSTRAINT fk_repo_ref_changes_task_run_id_task_runs_task_id_fk FOREIGN KEY (task_run_id) REFERENCES task_runs(task_id) ON DELETE CASCADE,
	CONSTRAINT "repo_ref_changes_kind_check" CHECK("ref_kind" IN ('head', 'tag')),
	CONSTRAINT "repo_ref_changes_action_check" CHECK("action" IN ('create', 'update', 'delete'))
);

CREATE TABLE repo_refs_current (
	id integer PRIMARY KEY AUTOINCREMENT,
	repo_id integer NOT NULL,
	ref_name text NOT NULL,
	ref_kind text NOT NULL,
	current_hash text NOT NULL,
	status text NOT NULL,
	archive_ref_name text,
	first_seen_at text NOT NULL,
	last_seen_at text NOT NULL,
	last_hash_updated_at text NOT NULL,
	current_commit_authored_at text,
	current_commit_committed_at text,
	current_commit_author_name text,
	current_commit_author_email text,
	current_commit_message text,
	deleted_at text,
	created_at text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	updated_at text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	CONSTRAINT fk_repo_refs_current_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE,
	CONSTRAINT "repo_refs_current_kind_check" CHECK("ref_kind" IN ('head', 'tag')),
	CONSTRAINT "repo_refs_current_status_check" CHECK("status" IN ('active', 'deleted'))
);

CREATE TABLE repos (
	id integer PRIMARY KEY AUTOINCREMENT,
	source_id text NOT NULL,
	platform text NOT NULL,
	ref_id text NOT NULL,
	status text DEFAULT 'active' NOT NULL,
	name text NOT NULL,
	full_name text NOT NULL,
	owner text NOT NULL,
	description text,
	html_url text,
	clone_url text,
	ssh_url text,
	default_branch text,
	visibility text,
	is_private integer DEFAULT 0 NOT NULL,
	is_fork integer DEFAULT 0 NOT NULL,
	is_archived integer DEFAULT 0 NOT NULL,
	origin text DEFAULT '{}' NOT NULL,
	meta text NOT NULL,
	archive_repo_size_bytes integer,
	last_seen_at text NOT NULL,
	disabled_at text,
	created_at text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	updated_at text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	CONSTRAINT "repos_status_check" CHECK("status" IN ('active', 'auto_excluded')),
	CONSTRAINT "repos_origin_json_check" CHECK(json_valid("origin")),
	CONSTRAINT "repos_meta_json_check" CHECK(json_valid("meta"))
);

CREATE TABLE task_run_logs (
	id integer PRIMARY KEY AUTOINCREMENT,
	task_id text NOT NULL,
	event_type text NOT NULL,
	summary text,
	meta_json text,
	error_message text,
	created_at text NOT NULL,
	CONSTRAINT fk_task_run_logs_task_id_task_runs_task_id_fk FOREIGN KEY (task_id) REFERENCES task_runs(task_id) ON DELETE CASCADE
);

CREATE TABLE task_runs (
	task_id text PRIMARY KEY,
	parent_task_id text,
	job_id text NOT NULL,
	job_type text NOT NULL,
	name text NOT NULL,
	args_json text,
	status text NOT NULL,
	created_at text NOT NULL,
	started_at text NOT NULL,
	finished_at text,
	error_message text,
	last_progress_summary text,
	last_progress_meta_json text,
	updated_at text NOT NULL
);

CREATE UNIQUE INDEX issues_current_repo_issue_unique ON issues_current (repo_id,issue_id);
CREATE INDEX issues_current_repo_number_idx ON issues_current (repo_id,number);
CREATE INDEX issues_current_repo_state_idx ON issues_current (repo_id,state);
CREATE UNIQUE INDEX repo_ref_changes_repo_run_ref_unique ON repo_ref_changes (repo_id,task_run_id,ref_name);
CREATE INDEX repo_ref_changes_repo_created_at_idx ON repo_ref_changes (repo_id,created_at);
CREATE INDEX repo_ref_changes_task_run_id_idx ON repo_ref_changes (task_run_id);
CREATE INDEX repo_ref_changes_repo_ref_created_at_idx ON repo_ref_changes (repo_id,ref_name,created_at);
CREATE UNIQUE INDEX repo_refs_current_repo_ref_unique ON repo_refs_current (repo_id,ref_name);
CREATE INDEX repo_refs_current_repo_status_idx ON repo_refs_current (repo_id,status);
CREATE INDEX repo_refs_current_repo_kind_idx ON repo_refs_current (repo_id,ref_kind);
CREATE UNIQUE INDEX repos_source_ref_unique ON repos (source_id,ref_id);
CREATE INDEX repos_source_status_idx ON repos (source_id,status);
CREATE INDEX repos_source_full_name_idx ON repos (source_id,full_name);
CREATE INDEX task_run_logs_task_id_created_at_idx ON task_run_logs (task_id,created_at,id);
CREATE INDEX task_runs_started_at_idx ON task_runs (started_at);
CREATE INDEX task_runs_job_type_idx ON task_runs (job_type);
CREATE INDEX task_runs_parent_task_id_idx ON task_runs (parent_task_id);
