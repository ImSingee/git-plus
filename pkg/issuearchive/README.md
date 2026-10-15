# Local GitHub issue archive

This package stores metadata separately from code repositories at:

```
<dataDir>/metadata/<sourceID>/<GitHub repository database ID>.git
```

Archives are local-only bare Git repositories. The only branch is
`refs/heads/snapshots`, with one immutable commit for each content change. Runtime
operations use go-git and the local filesystem, never a Git subprocess. No remote
or authentication token is configured. Existing code repositories and archives
using Git alternates are rejected.

## Snapshot layout

```
README.md
manifest.json
repository.json
labels.json
milestones.json
issues/<GitHub issue database ID>/
  issue.json
  comments.json
  timeline.json
  state.json
  README.md
```

IDs and path components are validated; titles, repository names and issue numbers
never become filenames. JSON preserves unknown API fields and numeric precision.
Object keys and entity collections are deterministically ordered. The original
numeric JSON representations are retained rather than converted through
floating-point numbers. Markdown is derived: issue/comment bodies are fenced
plain text, and titles are escaped, so raw HTML and arbitrary Markdown links are
not rendered as active content.

The versioned manifest records API version, crawl times, safe recovery
watermarks, counts, and explicit coverage. SHA-256 checksums cover every exported
file except the manifest itself. Its `content_hash` is the SHA-256 of the compact
JSON-encoded, sorted `files` map. Git object hashes also cover the manifest, trees,
commits, and historical snapshots.

## Save and recovery contract

- Incremental input merges into the prior snapshot; omitted issues are retained.
- A successful full reconciliation marks omitted issues `unavailable`, retaining
  their last observed raw issue, comments and timeline. This does not assert that
  they were deleted. A later observation clears the marker.
- Explicit unavailable entries also retain previously observed content.
- Nil collections mean “not fetched”; nonnil empty collections mean “observed
  empty.” This applies to issue comments/timeline and repository labels/milestones.
- Complete collection fetches must succeed before calling `Save`; an interrupted
  fetch must not be presented as an authoritative empty or partial collection.
- Pull requests are rejected. Only issue API payloads belong in this archive.
- Runs that only change crawl times, watermarks or the full/incremental flag do
  not create commits. Thus database recovery can safely replay from an earlier
  stored cursor. Raw content changes, edits, state changes, repository renames and
  availability changes create commits.
- Archive objects are written before the snapshots reference is published by a
  unique-temporary-file/atomic-rename operation. Failed writes leave the prior
  snapshot readable. An OS advisory lock serializes writers across
  independent Store instances/processes and is automatically released on exit.
  The persistent `.issuearchive.lock` file must not be removed or replaced while
  the application is running. A killed publisher may leave an unused temporary
  file, which cannot prevent later saves. External Git writes to the private
  snapshots branch are unsupported. `AcquireLock` exposes the same advisory lock
  for callers coordinating archive and index operations through a separate file.
- `Load` verifies and returns all data needed to rebuild a disposable database
  index, without contacting GitHub. `History` reads checked manifests efficiently.
  `Verify` additionally checks all selected snapshot files and derived content.
- Empty revision or `latest` means the latest snapshot. Other revisions must be
  full lowercase 40-character commit IDs reachable on the snapshots branch.

Attachments are **links only**. Binaries and content deleted before the backup
first observed it cannot be recovered. Timeline coverage is limited to events
returned by GitHub's REST timeline API to the backup account.

## Export and integrity

`ExportZIP` verifies before writing any output, then emits the selected snapshot's
raw JSON, Markdown and manifest with safe relative regular-file paths. It does
not include complete Git history. Preserve the local bare Git repository to keep
all historical snapshots; copy it while no writer is active.

Checksums detect accidental corruption, not a malicious party who can rewrite
both data and checksums. Git hashes are integrity checks, not signatures.

To bound memory use, a snapshot permits up to one million files, 64 MiB per file
and 512 MiB total content. Archives are private to the application. Protect the
configured data directory appropriately, especially when backing up private
repositories.

Run package verification with:

```
go test ./pkg/issuearchive
go vet ./pkg/issuearchive
CGO_ENABLED=1 go test -race ./pkg/issuearchive
```
