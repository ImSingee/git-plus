// Package issuearchive stores GitHub issue metadata in an independent, local-only
// bare Git repository. Raw JSON is authoritative; Markdown is a safe, derived view.
package issuearchive

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	SchemaVersion           = 1
	SnapshotsRef            = "refs/heads/snapshots"
	AvailabilityAvailable   = "available"
	AvailabilityUnavailable = "unavailable"
)

var (
	ErrNoSnapshots     = errors.New("issue archive has no snapshots")
	ErrBusy            = errors.New("issue archive is locked by another writer")
	ErrInvalidRevision = errors.New("invalid issue archive revision")
)

type Repository struct {
	SourceID string `json:"source_id"`
	ID       string `json:"id"`
	NodeID   string `json:"node_id"`
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
	Private  bool   `json:"private"`
}

// Issue uses GitHub's stable numeric database ID, encoded as a string to avoid
// precision loss. A nil Comments or Timeline slice means not fetched; an empty,
// nonnil slice is an authoritative empty collection. Unavailable issues retain
// their previously observed content, without claiming they were deleted.
type Issue struct {
	ID           string            `json:"id"`
	Number       int64             `json:"number"`
	Raw          json.RawMessage   `json:"raw"`
	Comments     []json.RawMessage `json:"comments"`
	Timeline     []json.RawMessage `json:"timeline"`
	Availability string            `json:"availability"`
	MissingSince string            `json:"missing_since,omitempty"`
}

// Snapshot is a complete archived view when returned by Load. Save also accepts
// incremental input: omitted issues are retained; when Full is true, omitted
// issues are marked unavailable. Nil labels/milestones retain the previous set.
// Watermarks are saved with content changes only. Rebuilding an index from an
// older watermark can replay data, but never skips newly observed content.
type Snapshot struct {
	Repository          Repository        `json:"repository"`
	Issues              map[string]Issue  `json:"issues"`
	Labels              []json.RawMessage `json:"labels"`
	Milestones          []json.RawMessage `json:"milestones"`
	APIVersion          string            `json:"api_version"`
	StartedAt           time.Time         `json:"started_at"`
	CompletedAt         time.Time         `json:"completed_at"`
	IssuesSince         string            `json:"issues_since,omitempty"`
	CommentsSince       string            `json:"comments_since,omitempty"`
	LastFullReconcileAt string            `json:"last_full_reconcile_at,omitempty"`
	Full                bool              `json:"full"`
}

type Result struct {
	Commit       string
	Changed      bool
	CreatedAt    time.Time
	IssueCount   int
	CommentCount int
}

type SnapshotInfo struct {
	Commit       string    `json:"commit"`
	CreatedAt    time.Time `json:"created_at"`
	IssueCount   int       `json:"issue_count"`
	CommentCount int       `json:"comment_count"`
	Full         bool      `json:"full"`
}

// Manifest describes the data covered by one snapshot. Files contains SHA-256
// hashes of every exported file except manifest.json itself; ContentHash hashes
// the canonical Files map. Git additionally protects the manifest and history.
type Manifest struct {
	SchemaVersion       int               `json:"schema_version"`
	APIVersion          string            `json:"api_version"`
	StartedAt           time.Time         `json:"started_at"`
	CompletedAt         time.Time         `json:"completed_at"`
	IssuesSince         string            `json:"issues_since,omitempty"`
	CommentsSince       string            `json:"comments_since,omitempty"`
	LastFullReconcileAt string            `json:"last_full_reconcile_at,omitempty"`
	Full                bool              `json:"full"`
	IssueCount          int               `json:"issue_count"`
	CommentCount        int               `json:"comment_count"`
	Coverage            Coverage          `json:"coverage"`
	ContentHash         string            `json:"content_hash"`
	Files               map[string]string `json:"files"`
}

type Coverage struct {
	Issues             string `json:"issues"`
	Comments           string `json:"comments"`
	Timeline           string `json:"timeline"`
	Attachments        string `json:"attachments"`
	UnavailableContent string `json:"unavailable_content"`
}

func snapshotCoverage() Coverage {
	return Coverage{
		Issues:             "non-pull-request-issues",
		Comments:           "issue-comments",
		Timeline:           "REST-supported-events",
		Attachments:        "links-only",
		UnavailableContent: "retained-if-previously-observed",
	}
}
