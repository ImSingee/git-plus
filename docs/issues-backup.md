# GitHub Issues backups

## Enable and use

1. In **Configuration → Sources**, edit a GitHub source and enable **Back up GitHub Issues**. Existing sources default to off. The equivalent source YAML field is `issues_enabled: true`.
2. Run **Sync All**, synchronize that source, or wait for the existing cron schedule. After repository discovery/code backup, each active repository gets a separate `sync-issues` child task. Code and issue failures remain independently visible in task history.
3. Open a repository's **Issues** tab. It provides current search/state filters, descriptions/comments/timeline, historical detail selection, manual incremental/full sync, and verified snapshot ZIP export. The latest 100 snapshot commits are shown in the picker; the complete history remains in Git. The issue list is the latest indexed view, even when historical details are selected.

Sources use their existing encrypted token. A fine-grained GitHub token needs **Issues: read** and **Metadata: read** for selected repositories; the existing private-code backup additionally needs its existing repository/content access. Organization approval/SSO may also be required. The app never requests a write permission for issue backups. Do not place plaintext tokens in YAML, archives, URLs, logs, or task arguments.

### CLI

`--repo` is the local database repository ID, visible in `/repos/<id>`, not the GitHub issue number or GitHub repository ID.

```sh
# Queue on the running server. Authentication uses the existing PASSWORD env var.
git-plus issues sync --repo 42 --server http://127.0.0.1:8080
git-plus issues sync --repo 42 --full --server http://127.0.0.1:8080

# Offline/local operations do not contact GitHub or require a GitHub token.
git-plus issues verify --data-dir ./data --repo 42
git-plus issues export --data-dir ./data --repo 42 --output ./issues-42.zip
git-plus issues export --data-dir ./data --repo 42 --snapshot <full-commit-id> --output ./older.zip
git-plus issues reindex --data-dir ./data --repo 42
```

Export refuses to overwrite an existing output file. ZIP contains one selected snapshot, not the whole Git history. Reindex restores only the latest snapshot and is serialized against synchronization with an OS advisory lock; retry after a running sync finishes if it reports busy. Existing migrations and the repository inventory must exist. To recover a completely lost SQLite database, restore/recreate that inventory first, then reindex the corresponding repository archives.

## Storage design

```text
<data-dir>/
  config.yaml                         # existing encrypted source configuration
  db.sqlite                           # repository inventory, tasks, disposable Issues index
  repos/<source-id>/<repository-id>/   # existing code archive, unchanged
  metadata/<source-id>/<repository-id>.git/
  metadata/<source-id>/<repository-id>.git.sync.lock
  staging/issues/<source-id>/<repository-id>/
    checkpoint.json
    issue-<stable-issue-id>.json
```

The separate metadata repository is bare and local-only, with no remote. Its `refs/heads/snapshots` branch receives one commit only when observed content changes. SQLite is the browsing index and live checkpoint, not the only copy of issue content. This keeps ordinary Git history/diff inspection available without mixing application-generated files into the source-code archive. Copy the entire metadata archive, configuration, and inventory while the server is stopped to preserve history; protect any backup copy at least as strictly as the original private repositories.

Each metadata commit has:

```text
README.md
manifest.json
repository.json
labels.json
milestones.json
issues/<stable-GitHub-database-ID>/
  issue.json
  comments.json
  timeline.json
  state.json
  README.md
```

Canonical JSON preserves unknown response fields and integer precision. IDs are strings in the index and manifest paths, while original JSON numbers retain their representation. Filenames never derive from titles or owner/repository names. Repository identity is verified against GitHub's numeric ID before publication; renames update identity metadata without moving the archive. A transferred issue can appear in a new repository's archive under the same stable issue ID, while the old archive retains its previous observations.

The versioned manifest records API version (`2022-11-28`), observation start/completion times, recovery watermarks, counts, coverage, and SHA-256 file checksums. Git commits also protect the manifest/tree. These are corruption checks, not signatures against a malicious archive editor. See [archive format and invariants](../pkg/issuearchive/README.md).

## Coverage and fidelity

Included: open and closed **issues**, their current descriptions/state/authors/assignees/labels/milestones, complete paginated comments, currently visible timeline events, and repository-wide labels and open/closed milestones. Pull-request entries returned by the Issues endpoint are excluded, including their ordinary comments. Raw API objects are authoritative; readable Markdown is derived with untrusted bodies fenced as text. The Web UI also escapes content and does not render raw HTML or fetch embedded images.

Attachments are **links only**. No attachment bytes are downloaded, authenticated links may expire, and images/files may not work offline. No PR review data, Discussions, Projects fields, full reaction-member lists, unavailable timeline events, webhook event log, or historical body revisions predating the first observation are promised. Git stores the states this backup actually observed; polling cannot capture every intermediate edit. A crawl is an observation window, not a transactional GitHub point-in-time snapshot.

There is no GitHub restore/import command. The public API cannot recreate original issue numbers, authors, timestamps, or every historical event faithfully. Archive export is for reading, analysis, and future explicit migration tooling. There is no automatic remote push of metadata repositories.

## Incremental sync and failure handling

- The first run is full. Later runs use independent issue and repository-comment `since` watermarks, with a five-minute overlap. A watermark is the run's **start**, never the newest item timestamp or completion time.
- Changes to comments can cause a thread to be refreshed even if its issue's `updated_at` did not change. Each selected thread fetches all comments and timeline pages. Labels and milestones are fully refreshed on every run. A full reconciliation rechecks every issue at least every seven days **when a sync runs**, or immediately via **Full reconcile**. There is no separate background schedule when cron is disabled.
- All list endpoints follow validated same-origin `Link: rel="next"` pagination. The API client pins its version, serializes requests per credential, bounds response/ETag-cache size, and rejects cross-origin redirects/next links. Network/5xx/short rate-limit errors get bounded retries. Longer rate-limit waits fail the task with the retry time, keeping the prior backup; retry manually or on the next scheduled run after that time. Known reset times are shared across client instances, so other queued repositories using that credential do not make requests before the reset.
- Successful per-thread results and the original observation window are checkpointed in private staging. An interrupted run re-enumerates pages and resumes matching completed threads. It does not trust old page offsets against a moving API dataset. In-progress jobs are requeued on server startup; queued-but-not-started tasks follow the existing in-memory task queue behavior and can be recreated by the next source sync.
- Git objects are written before the snapshot reference is atomically published. Only then does one SQLite transaction replace the index and advance watermarks. If publication succeeded but the index commit did not, the next sync rebuilds from Git. No-op runs advance SQLite watermarks without generating metadata-only commits; reindex may safely replay from an older archived watermark.
- A failed page, permission error, repository identity mismatch, 404/410, or partial collection never publishes a purported complete snapshot or advances successful watermarks. A **successful full crawl** that does not see a formerly archived issue marks it `unavailable`, retaining its last content and all prior commits. That marker deliberately does not claim deletion: transfer, visibility, and permissions can also explain absence. A later observation clears it. Deleted comments/labels can disappear from the current complete collection while remaining in old commits.
- OS advisory locks are released on process exit. Persistent lockfiles must not be removed while the application is running. Do not mutate the private snapshot branch using external Git tools or run two servers on one data directory.

## Security and operational limits

Private issue bodies/comments and staging files are private data even when the source Git repository is public. Archives do not contain configured credentials, but user-written issue content may itself contain secrets; the backup preserves that content. Keep the application data directory and exports access-controlled, keep API authentication enabled, use TLS when exposing the dashboard, and do not publish metadata archives unintentionally. Application SQLite directory permissions inherit the existing product's storage policy; encryption at rest is an operator responsibility.

The archive currently bounds one snapshot to 512 MiB total content, 64 MiB per file, and one million files; the API bounds each response to 32 MiB. This MVP materializes snapshot content in memory, so very large issue collections need a later streaming/storage iteration. Export uses a private temporary file and verifies the archive before serving it. The history picker is intentionally bounded, and server-side filtering currently searches issue titles/numbers rather than full comment text.

## Implementation map

- `pkg/githubapi`: credential-scoped, read-only API client and pagination/retry policy.
- `pkg/issuearchive`: canonical format, independent Git history, checksums, safe Markdown, ZIP, advisory locks.
- `pkg/issuesync`: full/incremental crawl, staging/recovery, atomic SQLite index rebuild.
- `pkg/issuetask`: existing task queue integration and interrupted-job restart.
- `pkg/issueservice`, `proto/gitplus/issue/v1`: ConnectRPC and authenticated ZIP route.
- `pkg/taskservice`: source/sync-all/cron child-task integration.
- `pkg/app/issues.go`: sync/export/verify/reindex CLI.
- `frontend/src/components/issues`, `frontend/src/lib/issue-*`: current/history browsing and export.
- `db/src/schema.ts`, `db/queries/issues.sql`: disposable issue index and live sync status. Generated migration/schema/sqlc and RPC code follow the repository's normal generators; Buf plugins are pinned to the pre-existing generated versions for reproducibility.

Further phases can add attachment byte capture with allowlisted hosts/SSRF controls/deduplication, archive streaming, richer search/history pagination, and explicit destination-specific import. These are not included in this feature.
