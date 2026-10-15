package issuearchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var testTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func testSnapshot() Snapshot {
	return Snapshot{
		Repository: Repository{SourceID: "source-1", ID: "123456", NodeID: "R_example", FullName: "owner/repo", HTMLURL: "https://github.com/owner/repo", Private: true},
		APIVersion: "2022-11-28", StartedAt: testTime, CompletedAt: testTime.Add(time.Minute), Full: true,
		IssuesSince: "2026-10-15T12:00:00Z", CommentsSince: "2026-10-15T12:00:00Z", LastFullReconcileAt: "2026-10-15T12:00:00Z",
		Issues: map[string]Issue{"9007199254740993": {
			ID: "9007199254740993", Number: 1,
			Raw:      json.RawMessage(`{"id":9007199254740993,"number":1,"title":"First issue","body":"Body with [attachment](https://example.com/file.png)","state":"open","unknown":{"large":1844674407370955161600001}}`),
			Comments: []json.RawMessage{json.RawMessage(`{"id":9007199254740995,"body":"Comment","user":{"login":"writer"}}`)},
			Timeline: []json.RawMessage{json.RawMessage(`{"id":12,"event":"labeled","created_at":"2026-10-15T12:00:00Z","unknown":"preserved"}`)},
		}},
		Labels:     []json.RawMessage{json.RawMessage(`{"id":7,"name":"bug"}`)},
		Milestones: []json.RawMessage{},
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path, err := Path(t.TempDir(), "source-1", "123456")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func saveTest(t *testing.T, s *Store, snapshot Snapshot) Result {
	t.Helper()
	result, err := s.Save(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRoundTripCanonicalJSONAndManifest(t *testing.T) {
	s := newTestStore(t)
	initial := testSnapshot()
	result := saveTest(t, s, initial)
	if !result.Changed || len(result.Commit) != 40 || result.IssueCount != 1 || result.CommentCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	loaded, err := s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	issue := loaded.Issues["9007199254740993"]
	if !bytes.Contains(issue.Raw, []byte(`1844674407370955161600001`)) || !bytes.Contains(issue.Raw, []byte(`9007199254740993`)) || !bytes.Contains(issue.Raw, []byte(`"unknown"`)) {
		t.Fatalf("raw JSON lost data: %s", issue.Raw)
	}
	if issue.Availability != AvailabilityAvailable || len(issue.Comments) != 1 || len(issue.Timeline) != 1 {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	if loaded.Repository != initial.Repository || loaded.IssuesSince != initial.IssuesSince || !loaded.CompletedAt.Equal(initial.CompletedAt) {
		t.Fatalf("metadata did not round trip: %+v", loaded)
	}
	if err := s.Verify(context.Background(), result.Commit); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Core.IsBare || len(cfg.Remotes) != 0 {
		t.Fatal("archive is not local-only bare Git")
	}
	head, err := s.repo.Head()
	if err != nil || head.Hash().String() != result.Commit {
		t.Fatalf("HEAD does not point to snapshots: %v, %v", head, err)
	}
	reopened, err := NewStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Verify(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestUnchangedRunsDoNotCommitOrAdvanceStoredWatermarks(t *testing.T) {
	s := newTestStore(t)
	first := saveTest(t, s, testSnapshot())
	again := testSnapshot()
	again.Full = false
	again.StartedAt = testTime.Add(time.Hour)
	again.CompletedAt = testTime.Add(2 * time.Hour)
	again.IssuesSince = "2026-10-15T14:00:00Z"
	again.CommentsSince = again.IssuesSince
	again.LastFullReconcileAt = again.IssuesSince
	issue := again.Issues["9007199254740993"]
	issue.Raw = json.RawMessage(`{"unknown":{"large":1844674407370955161600001},"body":"Body with [attachment](https://example.com/file.png)","title":"First issue","number":1,"state":"open","id":9007199254740993}`)
	again.Issues[issue.ID] = issue
	result := saveTest(t, s, again)
	if result.Changed || result.Commit != first.Commit || !result.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("no-op created a commit: %+v", result)
	}
	loaded, err := s.Load(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IssuesSince != testSnapshot().IssuesSince {
		t.Fatal("no-op must retain the earlier safe recovery watermark")
	}
	history, err := s.History(context.Background(), 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %+v, %v", history, err)
	}
}

func TestEditsRetainImmutableHistoryAndOldRevisions(t *testing.T) {
	s := newTestStore(t)
	first := saveTest(t, s, testSnapshot())
	changed := testSnapshot()
	issue := changed.Issues["9007199254740993"]
	issue.Raw = bytes.ReplaceAll(issue.Raw, []byte("First issue"), []byte("Edited issue"))
	issue.Comments = []json.RawMessage{}
	changed.Issues[issue.ID] = issue
	changed.CompletedAt = testTime.Add(time.Hour)
	second := saveTest(t, s, changed)
	if !second.Changed || first.Commit == second.Commit || second.CommentCount != 0 {
		t.Fatalf("edit not archived: %+v", second)
	}
	old, err := s.Load(context.Background(), first.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(old.Issues[issue.ID].Raw, []byte("First issue")) || len(old.Issues[issue.ID].Comments) != 1 {
		t.Fatal("previous snapshot changed")
	}
	latest, err := s.Load(context.Background(), second.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(latest.Issues[issue.ID].Raw, []byte("Edited issue")) || len(latest.Issues[issue.ID].Comments) != 0 {
		t.Fatal("edit not visible")
	}
	history, err := s.History(context.Background(), 0)
	if err != nil || len(history) != 2 || history[0].Commit != second.Commit || history[1].Commit != first.Commit {
		t.Fatalf("history: %+v, %v", history, err)
	}
	limited, err := s.History(context.Background(), 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limited history: %+v %v", limited, err)
	}
}

func TestMissingIssuesRetainContentAndRecover(t *testing.T) {
	s := newTestStore(t)
	original := testSnapshot()
	first := saveTest(t, s, original)
	incremental := original
	incremental.Full = false
	incremental.Issues = nil
	incremental.Labels = nil
	incremental.Milestones = nil
	noChange := saveTest(t, s, incremental)
	if noChange.Changed || noChange.Commit != first.Commit {
		t.Fatal("incremental omission modified archive")
	}
	full := incremental
	full.Full = true
	full.CompletedAt = testTime.Add(time.Hour)
	missing := saveTest(t, s, full)
	if !missing.Changed {
		t.Fatal("full reconciliation did not mark unavailable")
	}
	loaded, err := s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	issue := loaded.Issues["9007199254740993"]
	if issue.Availability != AvailabilityUnavailable || issue.MissingSince != full.CompletedAt.Format(time.RFC3339Nano) || len(issue.Raw) == 0 || len(issue.Comments) != 1 || len(issue.Timeline) != 1 {
		t.Fatalf("unavailable issue content not retained: %+v", issue)
	}
	full.CompletedAt = testTime.Add(2 * time.Hour)
	repeated := saveTest(t, s, full)
	if repeated.Changed {
		t.Fatal("repeated unavailability should be a no-op")
	}
	recovered := saveTest(t, s, original)
	if !recovered.Changed {
		t.Fatal("recovery was not recorded")
	}
	loaded, err = s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Issues[issue.ID].Availability != AvailabilityAvailable || loaded.Issues[issue.ID].MissingSince != "" {
		t.Fatal("issue did not recover")
	}
	explicit := original
	explicit.Full = false
	explicit.Issues = map[string]Issue{issue.ID: {ID: issue.ID, Availability: AvailabilityUnavailable}}
	saveTest(t, s, explicit)
	loaded, err = s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Issues[issue.ID].Comments) != 1 || loaded.Issues[issue.ID].Number != 1 {
		t.Fatal("explicit unavailable marker did not retain content")
	}
}

func TestNilCollectionsRetainAndEmptyCollectionsClear(t *testing.T) {
	s := newTestStore(t)
	original := testSnapshot()
	saveTest(t, s, original)
	incremental := testSnapshot()
	issue := incremental.Issues["9007199254740993"]
	issue.Comments = nil
	issue.Timeline = nil
	incremental.Issues[issue.ID] = issue
	incremental.Labels = nil
	incremental.Milestones = nil
	if result := saveTest(t, s, incremental); result.Changed {
		t.Fatal("nil collections did not retain content")
	}
	incremental.Labels = []json.RawMessage{}
	issue.Comments = []json.RawMessage{}
	issue.Timeline = []json.RawMessage{}
	incremental.Issues[issue.ID] = issue
	if result := saveTest(t, s, incremental); !result.Changed {
		t.Fatal("explicit empty collections not recorded")
	}
	loaded, err := s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Labels) != 0 || len(loaded.Issues[issue.ID].Comments) != 0 || len(loaded.Issues[issue.ID].Timeline) != 0 {
		t.Fatal("collections were not cleared")
	}
}

func TestRejectUnsafeIdentifiersAndRevisions(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../escape", "a/b", `a\b`, "a:b", "a\x00b", "你好", strings.Repeat("a", 129)} {
		if _, err := Path(t.TempDir(), id, "123"); err == nil {
			t.Errorf("accepted unsafe source ID %q", id)
		}
	}
	for _, id := range []string{"", "0", "01", "-1", ".", "../escape", "1/2", `1\2`, "1.0", "1e3", strings.Repeat("1", 41)} {
		if _, err := Path(t.TempDir(), "source", id); err == nil {
			t.Errorf("accepted unsafe repository ID %q", id)
		}
	}
	path, err := Path("/data", "source_ab-12", "9007199254740993")
	if err != nil || path != filepath.Join("/data", "metadata", "source_ab-12", "9007199254740993.git") {
		t.Fatalf("path: %s %v", path, err)
	}
	s := newTestStore(t)
	saveTest(t, s, testSnapshot())
	for _, revision := range []string{"HEAD", "HEAD~1", "refs/heads/snapshots", "../config", "aabbcc", strings.Repeat("f", 40), strings.Repeat("A", 40)} {
		if _, err := s.Load(context.Background(), revision); !errors.Is(err, ErrInvalidRevision) {
			t.Errorf("revision %q: got %v", revision, err)
		}
	}
	bad := testSnapshot()
	bad.Issues = map[string]Issue{"../evil": {ID: "../evil", Number: 1, Raw: json.RawMessage(`{}`)}}
	if _, err := s.Save(context.Background(), bad); err == nil {
		t.Fatal("accepted unsafe issue ID")
	}
}

func TestValidateRawJSONAndRepositoryIdentity(t *testing.T) {
	s := newTestStore(t)
	first := saveTest(t, s, testSnapshot())
	for _, raw := range []string{
		`{"id":2,"number":1}`, `{"id":9007199254740993,"number":2}`, `{"id":9007199254740993,"number":1,"pull_request":{}}`,
		`{"id":9007199254740993,"number":1} {}`, `null`, `[]`, `{invalid}`,
	} {
		snapshot := testSnapshot()
		issue := snapshot.Issues["9007199254740993"]
		issue.Raw = json.RawMessage(raw)
		snapshot.Issues[issue.ID] = issue
		if _, err := s.Save(context.Background(), snapshot); err == nil {
			t.Errorf("accepted invalid raw %s", raw)
		}
	}
	snapshot := testSnapshot()
	snapshot.Repository.ID = "654321"
	if _, err := s.Save(context.Background(), snapshot); err == nil {
		t.Fatal("allowed repository identity change")
	}
	head, err := s.latest()
	if err != nil || head.String() != first.Commit {
		t.Fatal("failed save moved branch")
	}
}

func TestMarkdownIsInertAndTitleNeverUsedAsPath(t *testing.T) {
	s := newTestStore(t)
	snapshot := testSnapshot()
	issue := snapshot.Issues["9007199254740993"]
	issue.Raw = json.RawMessage(`{"id":9007199254740993,"number":1,"title":"../../evil <script>title</script>\n# injected","body":"<script>alert(1)</script>\n\n\x60\x60\x60\n[click](javascript:alert(1))"}`)
	// JSON uses Unicode escapes for backticks, not invalid JSON \x escapes.
	issue.Raw = bytes.ReplaceAll(issue.Raw, []byte(`\x60`), []byte(`\u0060`))
	issue.Comments = []json.RawMessage{json.RawMessage(`{"id":1,"body":"<img src=x onerror=alert(1)>\n\u0060\u0060\u0060\u0060"}`)}
	snapshot.Issues[issue.ID] = issue
	result := saveTest(t, s, snapshot)
	_, _, files, err := s.readSnapshot(context.Background(), plumbing.NewHash(result.Commit))
	if err != nil {
		t.Fatal(err)
	}
	markdown := string(files["issues/"+issue.ID+"/README.md"])
	if !strings.Contains(markdown, "&lt;script&gt;title&lt;/script&gt;") {
		t.Fatal("title HTML was not escaped")
	}
	if !strings.Contains(markdown, "````text\n<script>alert(1)</script>") || !strings.Contains(markdown, "`````text\n<img") {
		t.Fatalf("bodies were not safely fenced:\n%s", markdown)
	}
	if strings.Contains(markdown, "\n# injected") {
		t.Fatal("title injected a Markdown heading")
	}
	for name := range files {
		if strings.Contains(name, "evil") || !validArchivePath(name) {
			t.Fatalf("title became a path: %s", name)
		}
	}
}

func TestZIPIsSelfContainedDeterministicAndChecksummed(t *testing.T) {
	s := newTestStore(t)
	result := saveTest(t, s, testSnapshot())
	var output bytes.Buffer
	if err := s.ExportZIP(context.Background(), &output, result.Commit); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := s.ExportZIP(context.Background(), &second, result.Commit); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), second.Bytes()) {
		t.Fatal("ZIP is not deterministic")
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range reader.File {
		if !validArchivePath(entry.Name) || entry.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name, ".git") {
			t.Fatalf("unsafe export entry: %s", entry.Name)
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = data
	}
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Coverage.Attachments != "links-only" || len(manifest.Files) != len(files)-1 {
		t.Fatalf("invalid manifest: %+v", manifest)
	}
	for name, hash := range manifest.Files {
		if sha256Hex(files[name]) != hash {
			t.Errorf("export checksum mismatch for %s", name)
		}
	}
	for _, name := range []string{"README.md", "repository.json", "labels.json", "milestones.json", "issues/9007199254740993/issue.json", "issues/9007199254740993/comments.json", "issues/9007199254740993/timeline.json", "issues/9007199254740993/state.json", "issues/9007199254740993/README.md"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing exported file %s", name)
		}
	}
	if !bytes.Contains(files["README.md"], []byte("snapshot only")) {
		t.Fatal("export scope not documented")
	}
}

func TestCorruptionDetectedBeforeExportOrSave(t *testing.T) {
	s := newTestStore(t)
	result := saveTest(t, s, testSnapshot())
	commit, err := s.repo.CommitObject(plumbing.NewHash(result.Commit))
	if err != nil {
		t.Fatal(err)
	}
	file, err := commit.File("issues/9007199254740993/issue.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded := s.repo.Storer.NewEncodedObject()
	encoded.SetType(plumbing.BlobObject)
	replacement := []byte("corrupt JSON\n")
	encoded.SetSize(int64(len(replacement)))
	writer, err := encoded.Writer()
	if err != nil {
		t.Fatal(err)
	}
	writer.Write(replacement)
	writer.Close()
	replacementHash, err := s.repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	objectPath := func(hash plumbing.Hash) string {
		h := hash.String()
		return filepath.Join(s.path, "objects", h[:2], h[2:])
	}
	replacementBytes, err := os.ReadFile(objectPath(replacementHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objectPath(file.Hash), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath(file.Hash), replacementBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(context.Background(), ""); err == nil {
		t.Fatal("did not detect corrupt Git object")
	}
	var output bytes.Buffer
	if err := s.ExportZIP(context.Background(), &output, ""); err == nil {
		t.Fatal("exported a corrupt snapshot")
	}
	if output.Len() != 0 {
		t.Fatal("wrote ZIP bytes before verifying snapshot")
	}
	if _, err := s.Save(context.Background(), testSnapshot()); err == nil {
		t.Fatal("saved on top of corruption")
	}
}

func TestManifestTamperingAndUnsafeTreesAreRejected(t *testing.T) {
	for _, kind := range []string{"checksum", "extra-file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			result := saveTest(t, s, testSnapshot())
			old := plumbing.NewHash(result.Commit)
			_, _, files, err := s.readSnapshot(context.Background(), old)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "checksum":
				files["README.md"] = []byte("tampered")
			case "extra-file":
				files["unexpected.txt"] = []byte("unexpected")
				var m Manifest
				json.Unmarshal(files["manifest.json"], &m)
				m.Files["unexpected.txt"] = sha256Hex(files["unexpected.txt"])
				m.ContentHash = filesHash(m.Files)
				files["manifest.json"], err = encodeJSON(m)
				if err != nil {
					t.Fatal(err)
				}
			}
			hash, err := s.writeTree(context.Background(), files)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				tree, err := s.repo.TreeObject(hash)
				if err != nil {
					t.Fatal(err)
				}
				tree.Entries[0].Mode = filemode.Symlink
				encoded := s.repo.Storer.NewEncodedObject()
				if err := tree.Encode(encoded); err != nil {
					t.Fatal(err)
				}
				hash, err = s.repo.Storer.SetEncodedObject(encoded)
				if err != nil {
					t.Fatal(err)
				}
			}
			commit := &object.Commit{TreeHash: hash, ParentHashes: []plumbing.Hash{old}, Author: object.Signature{Name: "test", When: testTime}, Committer: object.Signature{Name: "test", When: testTime}}
			encoded := s.repo.Storer.NewEncodedObject()
			if err := commit.Encode(encoded); err != nil {
				t.Fatal(err)
			}
			next, err := s.repo.Storer.SetEncodedObject(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.updateRef(old, next); err != nil {
				t.Fatal(err)
			}
			if err := s.Verify(context.Background(), ""); err == nil {
				t.Fatal("accepted tampered snapshot")
			}
			if err := s.Verify(context.Background(), result.Commit); err != nil {
				t.Fatalf("prior snapshot harmed: %v", err)
			}
		})
	}
}

func TestWriterLockAndCancellationPreserveLastSnapshot(t *testing.T) {
	s := newTestStore(t)
	first := saveTest(t, s, testSnapshot())
	release, err := lock(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testSnapshot()); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock result: %v", err)
	}
	if _, err := NewStore(s.path); err != nil {
		t.Fatalf("opening a reader during a write lock failed: %v", err)
	}
	release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Save(ctx, testSnapshot()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result: %v", err)
	}
	if _, err := s.Load(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("load cancellation result: %v", err)
	}
	var output bytes.Buffer
	if err := s.ExportZIP(ctx, &output, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("export cancellation result: %v", err)
	}
	head, err := s.latest()
	if err != nil || head.String() != first.Commit {
		t.Fatal("failure moved branch")
	}
}

func TestEmptyStoreAndForeignRepositories(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Load(context.Background(), ""); !errors.Is(err, ErrNoSnapshots) {
		t.Fatalf("empty load: %v", err)
	}
	history, err := s.History(context.Background(), 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("empty history: %v %v", history, err)
	}
	foreign := filepath.Join(t.TempDir(), "code.git")
	if _, err := git.PlainInit(foreign, true); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(foreign); err == nil {
		t.Fatal("opened code archive as metadata archive")
	}
	if _, err := s.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"https://example.com/repo.git"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.path); err == nil {
		t.Fatal("opened archive containing a remote")
	}
	nonempty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonempty, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(nonempty); err == nil {
		t.Fatal("initialized nonempty directory")
	}
}

func TestRepositoryRenameAndCollectionReordering(t *testing.T) {
	s := newTestStore(t)
	first := testSnapshot()
	issue := first.Issues["9007199254740993"]
	issue.Comments = append(issue.Comments, json.RawMessage(`{"id":2,"body":"Earlier"}`))
	first.Issues[issue.ID] = issue
	saveTest(t, s, first)
	issue.Comments[0], issue.Comments[1] = issue.Comments[1], issue.Comments[0]
	first.Issues[issue.ID] = issue
	if result := saveTest(t, s, first); result.Changed {
		t.Fatal("collection reordering created a commit")
	}
	first.Repository.FullName = "new-owner/new-name"
	first.Repository.HTMLURL = "https://github.com/new-owner/new-name"
	if result := saveTest(t, s, first); !result.Changed {
		t.Fatal("repository rename not archived")
	}
	if !strings.HasSuffix(s.path, filepath.Join("source-1", "123456.git")) {
		t.Fatalf("rename changed stable path: %s", s.path)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("out of space") }

func TestExportPropagatesWriterFailure(t *testing.T) {
	s := newTestStore(t)
	saveTest(t, s, testSnapshot())
	if err := s.ExportZIP(context.Background(), failingWriter{}, ""); err == nil {
		t.Fatal("ignored write failure")
	}
	if err := s.Verify(context.Background(), ""); err != nil {
		t.Fatal("failed export damaged archive")
	}
}

func TestAtomicPublicationFailureKeepsPriorSnapshot(t *testing.T) {
	s := newTestStore(t)
	first := saveTest(t, s, testSnapshot())
	current := plumbing.NewHash(first.Commit)
	// A stale expected head must never replace an existing snapshot.
	if err := s.updateRef(plumbing.NewHash(strings.Repeat("f", 40)), current); err == nil {
		t.Fatal("accepted stale expected reference")
	}
	head, err := s.latest()
	if err != nil || head != current {
		t.Fatal("failed publication changed latest snapshot")
	}
	if err := s.Verify(context.Background(), first.Commit); err != nil {
		t.Fatal(err)
	}
	// A leftover unique temporary file from a killed writer cannot block retries.
	if err := os.WriteFile(filepath.Join(s.path, ".snapshots-crashed"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := testSnapshot()
	issue := changed.Issues["9007199254740993"]
	issue.Raw = bytes.ReplaceAll(issue.Raw, []byte("First issue"), []byte("Changed issue"))
	changed.Issues[issue.ID] = issue
	if result := saveTest(t, s, changed); !result.Changed {
		t.Fatal("retry did not publish snapshot")
	}
	if err := os.Remove(filepath.Join(s.path, ".snapshots-crashed")); err != nil {
		t.Fatal(err)
	}
	refs, err := s.repo.References()
	if err != nil {
		t.Fatal(err)
	}
	defer refs.Close()
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name() != plumbing.HEAD && ref.Name().String() != SnapshotsRef {
			return fmt.Errorf("unexpected reference %s", ref.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdvisoryLockReleasedWhenProcessExits(t *testing.T) {
	if path := os.Getenv("ISSUEARCHIVE_TEST_LOCK"); path != "" {
		if _, err := AcquireLock(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		// Deliberately exit without invoking release, as a killed application would.
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "locks", "sync.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestAdvisoryLockReleasedWhenProcessExits$")
	command.Env = append(os.Environ(), "ISSUEARCHIVE_TEST_LOCK="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lock subprocess: %s: %v", output, err)
	}
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("process exit left a stale lock: %v", err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock should be busy: %v", err)
	}
	release()
	release()
	again, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestConcurrentReadersSeeOnlyCompleteSnapshots(t *testing.T) {
	s := newTestStore(t)
	saveTest(t, s, testSnapshot())
	independent, err := NewStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 40)
	for _, reader := range []*Store{s, independent} {
		wg.Add(1)
		go func(reader *Store) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				snapshot, err := reader.Load(context.Background(), "")
				if err != nil {
					failures <- err
					return
				}
				if len(snapshot.Issues) != 1 || len(snapshot.Issues["9007199254740993"].Comments) != 1 {
					failures <- errors.New("reader observed partial snapshot")
					return
				}
			}
		}(reader)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			snapshot := testSnapshot()
			issue := snapshot.Issues["9007199254740993"]
			issue.Raw = bytes.ReplaceAll(issue.Raw, []byte("First issue"), []byte(fmt.Sprintf("Edit %d", i)))
			snapshot.Issues[issue.ID] = issue
			if _, err := s.Save(context.Background(), snapshot); err != nil {
				failures <- err
				return
			}
		}
	}()
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestRejectSymlinkArchivesAndAlternates(t *testing.T) {
	s := newTestStore(t)
	link := filepath.Join(t.TempDir(), "linked.git")
	if err := os.Symlink(s.path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewStore(link); err == nil {
		t.Fatal("opened symlink archive")
	}
	info := filepath.Join(s.path, "objects", "info")
	if err := os.MkdirAll(info, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte("/other/objects\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.path); err == nil {
		t.Fatal("opened archive dependent on an external object store")
	}
}

func TestCorruptUnreachableObjectCannotBePublished(t *testing.T) {
	s := newTestStore(t)
	snapshot, err := normalizeSnapshot(testSnapshot(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	raw := snapshot.Issues["9007199254740993"].Raw
	encoded := s.repo.Storer.NewEncodedObject()
	encoded.SetType(plumbing.BlobObject)
	encoded.SetSize(int64(len(raw)))
	writer, err := encoded.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := s.repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	h := hash.String()
	path := filepath.Join(s.path, "objects", h[:2], h[2:])
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted orphan from an earlier interrupted write"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testSnapshot()); err == nil {
		t.Fatal("published a corrupt, previously unreachable object")
	}
	if _, err := s.Load(context.Background(), ""); !errors.Is(err, ErrNoSnapshots) {
		t.Fatalf("failed save published reference: %v", err)
	}
}
