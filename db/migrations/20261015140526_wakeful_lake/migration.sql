CREATE TABLE `issue_sync_state` (
	`repo_id` integer PRIMARY KEY,
	`status` text DEFAULT 'idle' NOT NULL,
	`latest_commit` text DEFAULT '' NOT NULL,
	`issues_since` text DEFAULT '' NOT NULL,
	`comments_since` text DEFAULT '' NOT NULL,
	`last_successful_at` text DEFAULT '' NOT NULL,
	`last_full_reconcile_at` text DEFAULT '' NOT NULL,
	`last_error` text DEFAULT '' NOT NULL,
	`issue_count` integer DEFAULT 0 NOT NULL,
	`comment_count` integer DEFAULT 0 NOT NULL,
	`updated_at` text NOT NULL,
	CONSTRAINT `fk_issue_sync_state_repo_id_repos_id_fk` FOREIGN KEY (`repo_id`) REFERENCES `repos`(`id`) ON DELETE CASCADE
);
--> statement-breakpoint
CREATE TABLE `issues_current` (
	`repo_id` integer NOT NULL,
	`issue_id` text NOT NULL,
	`number` integer NOT NULL,
	`title` text NOT NULL,
	`state` text NOT NULL,
	`author_login` text NOT NULL,
	`updated_at` text NOT NULL,
	`availability` text NOT NULL,
	`labels_json` text DEFAULT '[]' NOT NULL,
	`assignees_json` text DEFAULT '[]' NOT NULL,
	CONSTRAINT `fk_issues_current_repo_id_repos_id_fk` FOREIGN KEY (`repo_id`) REFERENCES `repos`(`id`) ON DELETE CASCADE
);
--> statement-breakpoint
CREATE UNIQUE INDEX `issues_current_repo_issue_unique` ON `issues_current` (`repo_id`,`issue_id`);--> statement-breakpoint
CREATE INDEX `issues_current_repo_number_idx` ON `issues_current` (`repo_id`,`number`);--> statement-breakpoint
CREATE INDEX `issues_current_repo_state_idx` ON `issues_current` (`repo_id`,`state`);