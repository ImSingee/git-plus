package issuearchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Bounds apply equally on writing and reading, including archives modified by
// outside tools. They prevent malicious trees/blobs from exhausting memory.
const maxFileBytes = 64 << 20
const maxSnapshotBytes = 512 << 20
const maxSnapshotFiles = 1_000_000

type treeNode struct {
	files map[string][]byte
	dirs  map[string]*treeNode
}

func newTreeNode() *treeNode {
	return &treeNode{files: map[string][]byte{}, dirs: map[string]*treeNode{}}
}

func validArchivePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00\r\n") || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return false
		}
	}
	return true
}

func (s *Store) writeTree(ctx context.Context, files map[string][]byte) (plumbing.Hash, error) {
	root := newTreeNode()
	total := 0
	if len(files) > maxSnapshotFiles {
		return plumbing.ZeroHash, errors.New("snapshot contains too many files")
	}
	for name, data := range files {
		if !validArchivePath(name) {
			return plumbing.ZeroHash, fmt.Errorf("unsafe archive path %q", name)
		}
		total += len(data)
		if len(data) > maxFileBytes || total > maxSnapshotBytes {
			return plumbing.ZeroHash, errors.New("snapshot exceeds archive size limit")
		}
		parts := strings.Split(name, "/")
		node := root
		for _, part := range parts[:len(parts)-1] {
			if node.dirs[part] == nil {
				node.dirs[part] = newTreeNode()
			}
			node = node.dirs[part]
		}
		node.files[parts[len(parts)-1]] = data
	}
	return s.writeTreeNode(ctx, root)
}

func (s *Store) writeTreeNode(ctx context.Context, node *treeNode) (plumbing.Hash, error) {
	tree := &object.Tree{}
	for name, data := range node.files {
		if err := ctx.Err(); err != nil {
			return plumbing.ZeroHash, err
		}
		encoded := s.repo.Storer.NewEncodedObject()
		encoded.SetType(plumbing.BlobObject)
		encoded.SetSize(int64(len(data)))
		writer, err := encoded.Writer()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if _, err := writer.Write(data); err != nil {
			writer.Close()
			return plumbing.ZeroHash, err
		}
		if err := writer.Close(); err != nil {
			return plumbing.ZeroHash, err
		}
		hash, err := s.repo.Storer.SetEncodedObject(encoded)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if err := s.syncObject(hash); err != nil {
			return plumbing.ZeroHash, err
		}
		if _, _, err := s.checkedObject(hash, plumbing.BlobObject); err != nil {
			return plumbing.ZeroHash, err
		}
		tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: hash})
	}
	for name, child := range node.dirs {
		hash, err := s.writeTreeNode(ctx, child)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: hash})
	}
	sort.Sort(object.TreeEntrySorter(tree.Entries))
	encoded := s.repo.Storer.NewEncodedObject()
	if err := tree.Encode(encoded); err != nil {
		return plumbing.ZeroHash, err
	}
	hash, err := s.repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if err := s.syncObject(hash); err != nil {
		return plumbing.ZeroHash, err
	}
	if _, _, err := s.checkedObject(hash, plumbing.TreeObject); err != nil {
		return plumbing.ZeroHash, err
	}
	return hash, nil
}

func (s *Store) checkedObject(hash plumbing.Hash, kind plumbing.ObjectType) (plumbing.EncodedObject, []byte, error) {
	encoded, err := s.repo.Storer.EncodedObject(kind, hash)
	if err != nil {
		return nil, nil, fmt.Errorf("read Git object %s: %w", hash, err)
	}
	if encoded.Size() < 0 || encoded.Size() > maxFileBytes {
		return nil, nil, errors.New("Git object exceeds archive size limit")
	}
	reader, err := encoded.Reader()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
	closeErr := reader.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if int64(len(data)) != encoded.Size() || plumbing.ComputeHash(kind, data) != hash {
		return nil, nil, fmt.Errorf("Git object checksum mismatch: %s", hash)
	}
	return encoded, data, nil
}

func (s *Store) checkedCommit(hash plumbing.Hash) (*object.Commit, error) {
	encoded, _, err := s.checkedObject(hash, plumbing.CommitObject)
	if err != nil {
		return nil, err
	}
	commit, err := object.DecodeCommit(s.repo.Storer, encoded)
	if err != nil {
		return nil, err
	}
	if len(commit.ParentHashes) > 1 {
		return nil, errors.New("snapshot commits must have at most one parent")
	}
	return commit, nil
}

func (s *Store) readFiles(ctx context.Context, treeHash plumbing.Hash) (map[string][]byte, error) {
	files := map[string][]byte{}
	total := 0
	visitedEntries := 0
	var walk func(plumbing.Hash, string, int) error
	walk = func(hash plumbing.Hash, prefix string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 3 {
			return errors.New("snapshot tree is too deeply nested")
		}
		encoded, _, err := s.checkedObject(hash, plumbing.TreeObject)
		if err != nil {
			return err
		}
		tree, err := object.DecodeTree(s.repo.Storer, encoded)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, entry := range tree.Entries {
			visitedEntries++
			if visitedEntries > 2*maxSnapshotFiles {
				return errors.New("snapshot contains too many tree entries")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.Contains(entry.Name, "/") || !validArchivePath(entry.Name) || seen[entry.Name] {
				return errors.New("unsafe or duplicate Git tree entry")
			}
			seen[entry.Name] = true
			name := prefix + entry.Name
			switch entry.Mode {
			case filemode.Dir:
				if err := walk(entry.Hash, name+"/", depth+1); err != nil {
					return err
				}
			case filemode.Regular:
				if len(files) >= maxSnapshotFiles {
					return errors.New("snapshot contains too many files")
				}
				_, data, err := s.checkedObject(entry.Hash, plumbing.BlobObject)
				if err != nil {
					return err
				}
				total += len(data)
				if total > maxSnapshotBytes {
					return errors.New("snapshot exceeds archive size limit")
				}
				files[name] = data
			default:
				return errors.New("snapshot contains a symlink, submodule or non-regular file")
			}
		}
		return nil
	}
	if err := walk(treeHash, "", 0); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Store) readSnapshot(ctx context.Context, hash plumbing.Hash) (Snapshot, Manifest, map[string][]byte, error) {
	fail := func(err error) (Snapshot, Manifest, map[string][]byte, error) {
		return Snapshot{}, Manifest{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	commit, err := s.checkedCommit(hash)
	if err != nil {
		return fail(err)
	}
	files, err := s.readFiles(ctx, commit.TreeHash)
	if err != nil {
		return fail(err)
	}
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		return fail(fmt.Errorf("read manifest: %w", err))
	}
	if m.SchemaVersion != SchemaVersion {
		return fail(fmt.Errorf("unsupported issue archive schema version %d", m.SchemaVersion))
	}
	if m.Coverage != snapshotCoverage() {
		return fail(errors.New("unsupported or incomplete snapshot coverage"))
	}
	if m.CompletedAt.IsZero() || m.StartedAt.IsZero() || m.CompletedAt.Before(m.StartedAt) {
		return fail(errors.New("invalid snapshot timestamps"))
	}
	if len(m.Files) != len(files)-1 || m.ContentHash != filesHash(m.Files) {
		return fail(errors.New("manifest file list or content hash mismatch"))
	}
	if _, exists := m.Files["manifest.json"]; exists {
		return fail(errors.New("manifest must not checksum itself"))
	}
	for name, expected := range m.Files {
		data, ok := files[name]
		if !validArchivePath(name) || !ok || sha256Hex(data) != expected {
			return fail(fmt.Errorf("manifest checksum mismatch: %s", name))
		}
	}
	snapshot := Snapshot{Issues: map[string]Issue{}, APIVersion: m.APIVersion, StartedAt: m.StartedAt, CompletedAt: m.CompletedAt, IssuesSince: m.IssuesSince, CommentsSince: m.CommentsSince, LastFullReconcileAt: m.LastFullReconcileAt, Full: m.Full}
	if err := json.Unmarshal(files["repository.json"], &snapshot.Repository); err != nil {
		return fail(err)
	}
	if err := validateIdentity(snapshot.Repository.SourceID, snapshot.Repository.ID); err != nil {
		return fail(err)
	}
	if err := json.Unmarshal(files["labels.json"], &snapshot.Labels); err != nil {
		return fail(err)
	}
	if err := json.Unmarshal(files["milestones.json"], &snapshot.Milestones); err != nil {
		return fail(err)
	}
	comments := 0
	for name, data := range files {
		if !strings.HasPrefix(name, "issues/") || !strings.HasSuffix(name, "/state.json") {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) != 3 || !numericIDPattern.MatchString(parts[1]) {
			return fail(errors.New("invalid issue path"))
		}
		var state issueState
		if err := json.Unmarshal(data, &state); err != nil {
			return fail(err)
		}
		if state.ID != parts[1] {
			return fail(errors.New("issue ID does not match filename"))
		}
		prefix := "issues/" + state.ID + "/"
		issue := Issue{ID: state.ID, Number: state.Number, Raw: files[prefix+"issue.json"], Availability: state.Availability, MissingSince: state.MissingSince}
		if err := json.Unmarshal(files[prefix+"comments.json"], &issue.Comments); err != nil {
			return fail(err)
		}
		if err := json.Unmarshal(files[prefix+"timeline.json"], &issue.Timeline); err != nil {
			return fail(err)
		}
		normalized, err := normalizeIssue(issue)
		if err != nil {
			return fail(err)
		}
		snapshot.Issues[state.ID] = normalized
		comments += len(issue.Comments)
	}
	if m.IssueCount != len(snapshot.Issues) || m.CommentCount != comments {
		return fail(errors.New("manifest counts do not match snapshot"))
	}
	// Regeneration validates the canonical JSON, exact permitted file set, and
	// derived Markdown. Extra files and user-controlled export paths are rejected.
	expected, _, err := snapshotFiles(ctx, snapshot)
	if err != nil {
		return fail(err)
	}
	if len(files) != len(expected) {
		return fail(errors.New("unexpected snapshot files"))
	}
	for name, data := range expected {
		if !bytes.Equal(data, files[name]) {
			return fail(fmt.Errorf("noncanonical or invalid snapshot file: %s", name))
		}
	}
	return snapshot, m, files, nil
}

// ExportZIP writes a self-contained selected snapshot, including its manifest,
// raw JSON and Markdown. It deliberately does not include complete Git history.
// To preserve history, copy the local bare Git archive while no writer is active.
func (s *Store) ExportZIP(ctx context.Context, writer io.Writer, revision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if writer == nil {
		return errors.New("ZIP writer is required")
	}
	hash, err := s.resolve(ctx, revision)
	if err != nil {
		return err
	}
	_, manifest, files, err := s.readSnapshot(ctx, hash)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	archive := zip.NewWriter(writer)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: manifest.CompletedAt}
		header.SetMode(0600)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		data := files[name]
		for len(data) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			size := min(len(data), 32<<10)
			written, err := entry.Write(data[:size])
			if err != nil {
				return err
			}
			if written != size {
				return io.ErrShortWrite
			}
			data = data[written:]
		}
	}
	return archive.Close()
}

// readManifest lets history listings inspect immutable commit metadata without
// loading every issue body in every historical snapshot. Full integrity checks
// remain available through Verify and are always performed by Load and ExportZIP.
func (s *Store) readManifest(hash plumbing.Hash) (Manifest, *object.Commit, error) {
	commit, err := s.checkedCommit(hash)
	if err != nil {
		return Manifest{}, nil, err
	}
	encoded, _, err := s.checkedObject(commit.TreeHash, plumbing.TreeObject)
	if err != nil {
		return Manifest{}, nil, err
	}
	tree, err := object.DecodeTree(s.repo.Storer, encoded)
	if err != nil {
		return Manifest{}, nil, err
	}
	var manifestHash plumbing.Hash
	for _, entry := range tree.Entries {
		if entry.Name == "manifest.json" {
			if !manifestHash.IsZero() || entry.Mode != filemode.Regular {
				return Manifest{}, nil, errors.New("invalid manifest tree entry")
			}
			manifestHash = entry.Hash
		}
	}
	if manifestHash.IsZero() {
		return Manifest{}, nil, errors.New("snapshot manifest is missing")
	}
	_, data, err := s.checkedObject(manifestHash, plumbing.BlobObject)
	if err != nil {
		return Manifest{}, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, nil, err
	}
	if m.SchemaVersion != SchemaVersion || m.Coverage != snapshotCoverage() || m.CompletedAt.IsZero() || m.StartedAt.IsZero() || m.CompletedAt.Before(m.StartedAt) || m.IssueCount < 0 || m.CommentCount < 0 || m.ContentHash != filesHash(m.Files) {
		return Manifest{}, nil, errors.New("invalid snapshot manifest")
	}
	return m, commit, nil
}
