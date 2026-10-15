package issuearchive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var numericIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,39}$`)
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

const formatMarker = "git-plus-issue-archive-v1\n"

// Store never configures a remote, fetches, pushes, or executes a Git command.
// Its path is private to issue metadata and must not be an existing code archive.
type Store struct {
	mu   sync.Mutex // go-git filesystem storage has mutable internal caches.
	path string
	repo *git.Repository
}

// Path constructs the stable, title-independent path of an issue archive.
func Path(dataDir, sourceID, repositoryID string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("data directory is required")
	}
	if err := validateIdentity(sourceID, repositoryID); err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "metadata", sourceID, repositoryID+".git"), nil
}

func validateIdentity(sourceID, repositoryID string) error {
	if !sourceIDPattern.MatchString(sourceID) {
		return errors.New("invalid source ID: use 1-128 ASCII letters, digits, underscores or hyphens")
	}
	if !numericIDPattern.MatchString(repositoryID) {
		return errors.New("invalid GitHub repository ID")
	}
	return nil
}

// NewStore opens or initializes a local bare metadata repository. Existing
// repositories without our format marker are rejected to protect code archives.
func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("archive path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(absolute); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return nil, errors.New("archive path must be a directory, not a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	repo, err := git.PlainOpen(absolute)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		release, lockErr := lock(absolute)
		if lockErr != nil {
			return nil, lockErr
		}
		defer release()
		// Another initializer may have completed before the lock was acquired.
		repo, err = git.PlainOpen(absolute)
		if errors.Is(err, git.ErrRepositoryNotExists) {
			entries, readErr := os.ReadDir(absolute)
			if readErr != nil {
				return nil, readErr
			}
			for _, entry := range entries {
				if entry.Name() != ".issuearchive.lock" {
					return nil, errors.New("refusing to initialize a nonempty archive directory")
				}
			}
			repo, err = git.PlainInit(absolute, true)
			if err != nil {
				return nil, fmt.Errorf("initialize archive: %w", err)
			}
			if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName(SnapshotsRef))); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(absolute, "issuearchive-format"), []byte(formatMarker), 0600); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(absolute, "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("issue archive must not use alternate object stores")
	}
	marker, err := os.ReadFile(filepath.Join(absolute, "issuearchive-format"))
	if err != nil || string(marker) != formatMarker {
		return nil, errors.New("repository is not a git-plus issue archive")
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, err
	}
	if !cfg.Core.IsBare || len(cfg.Remotes) != 0 {
		return nil, errors.New("issue archive must be bare and have no remotes")
	}
	// Disable object caching so Verify checks the current on-disk bytes, even
	// after an object has already been read through this Store.
	repo, err = git.Open(filesystem.NewStorage(osfs.New(absolute), cache.NewObjectLRU(0)), nil)
	if err != nil {
		return nil, err
	}
	return &Store{path: absolute, repo: repo}, nil
}

func (s *Store) latest() (plumbing.Hash, error) {
	ref, err := s.repo.Reference(plumbing.ReferenceName(SnapshotsRef), false)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, ErrNoSnapshots
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if ref.Type() != plumbing.HashReference {
		return plumbing.ZeroHash, errors.New("snapshot branch is not a direct reference")
	}
	return ref.Hash(), nil
}

func (s *Store) Save(ctx context.Context, snapshot Snapshot) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	release, err := lock(s.path)
	if err != nil {
		return Result{}, err
	}
	defer release()
	old, err := s.latest()
	if err != nil && !errors.Is(err, ErrNoSnapshots) {
		return Result{}, err
	}
	var previous Snapshot
	var previousManifest Manifest
	if err == nil {
		previous, previousManifest, _, err = s.readSnapshot(ctx, old)
		if err != nil {
			return Result{}, fmt.Errorf("read previous snapshot: %w", err)
		}
	}
	snapshot, err = normalizeSnapshot(snapshot, previous)
	if err != nil {
		return Result{}, err
	}
	files, manifest, err := snapshotFiles(ctx, snapshot)
	if err != nil {
		return Result{}, err
	}
	if !old.IsZero() && manifest.ContentHash == previousManifest.ContentHash {
		return Result{Commit: old.String(), CreatedAt: previousManifest.CompletedAt, IssueCount: previousManifest.IssueCount, CommentCount: previousManifest.CommentCount}, nil
	}
	treeHash, err := s.writeTree(ctx, files)
	if err != nil {
		return Result{}, err
	}
	signature := object.Signature{Name: "git-plus", Email: "archive@localhost", When: snapshot.CompletedAt}
	commit := &object.Commit{Author: signature, Committer: signature, Message: "Snapshot GitHub issue metadata\n", TreeHash: treeHash}
	if !old.IsZero() {
		commit.ParentHashes = []plumbing.Hash{old}
	}
	encoded := s.repo.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		return Result{}, err
	}
	hash, err := s.repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		return Result{}, fmt.Errorf("write snapshot commit: %w", err)
	}
	if err := s.syncObject(hash); err != nil {
		return Result{}, err
	}
	if _, _, err := s.checkedObject(hash, plumbing.CommitObject); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := s.updateRef(old, hash); err != nil {
		return Result{}, err
	}
	return Result{Commit: hash.String(), Changed: true, CreatedAt: manifest.CompletedAt, IssueCount: manifest.IssueCount, CommentCount: manifest.CommentCount}, nil
}

// updateRef publishes a unique temporary file by atomic rename after all objects
// are durable. The archive advisory lock serializes writers; unique temporary
// names cannot block later saves after a crash. External writes to this private
// metadata branch are not supported.
func (s *Store) updateRef(old, next plumbing.Hash) error {
	refPath := filepath.Join(s.path, "refs", "heads", "snapshots")
	if err := os.MkdirAll(filepath.Dir(refPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.path, ".snapshots-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	current, err := s.latest()
	if err != nil && !errors.Is(err, ErrNoSnapshots) {
		f.Close()
		return err
	}
	if current != old {
		f.Close()
		return errors.New("snapshot branch changed concurrently")
	}
	if _, err := io.WriteString(f, next.String()+"\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, refPath); err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	// Directory sync is unavailable on some platforms. The atomic rename is still
	// required there; do not turn a published snapshot into a misleading failure.
	if dir, err := os.Open(filepath.Dir(refPath)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (s *Store) syncObject(hash plumbing.Hash) error {
	h := hash.String()
	if err := syncObjectFile(filepath.Join(s.path, "objects", h[:2], h[2:])); err != nil {
		return err
	}
	for _, name := range []string{filepath.Join(s.path, "objects", h[:2]), filepath.Join(s.path, "objects")} {
		if dir, err := os.Open(name); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return nil
}

// resolve only accepts a full immutable commit hash or the current snapshot.
// Git revision expressions, refs and unreachable objects are not exposed.
func (s *Store) resolve(ctx context.Context, revision string) (plumbing.Hash, error) {
	if revision != "" && revision != "latest" && !revisionPattern.MatchString(revision) {
		return plumbing.ZeroHash, ErrInvalidRevision
	}
	head, err := s.latest()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if revision == "" || revision == "latest" {
		return head, nil
	}
	target := plumbing.NewHash(revision)
	for current := head; !current.IsZero(); {
		if err := ctx.Err(); err != nil {
			return plumbing.ZeroHash, err
		}
		if current == target {
			return target, nil
		}
		commit, err := s.checkedCommit(current)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if len(commit.ParentHashes) == 0 {
			break
		}
		if len(commit.ParentHashes) != 1 {
			return plumbing.ZeroHash, errors.New("invalid snapshot ancestry")
		}
		current = commit.ParentHashes[0]
	}
	return plumbing.ZeroHash, ErrInvalidRevision
}

func (s *Store) Load(ctx context.Context, revision string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := s.resolve(ctx, revision)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, _, _, err := s.readSnapshot(ctx, hash)
	return snapshot, err
}

// History returns newest first. A nonpositive limit returns all snapshots.
func (s *Store) History(ctx context.Context, limit int) ([]SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := s.latest()
	if errors.Is(err, ErrNoSnapshots) {
		return []SnapshotInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []SnapshotInfo{}
	for !current.IsZero() && (limit <= 0 || len(result) < limit) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, commit, err := s.readManifest(current)
		if err != nil {
			return nil, err
		}
		result = append(result, SnapshotInfo{Commit: current.String(), CreatedAt: m.CompletedAt, IssueCount: m.IssueCount, CommentCount: m.CommentCount, Full: m.Full})
		if len(commit.ParentHashes) == 0 {
			break
		}
		if len(commit.ParentHashes) != 1 {
			return nil, errors.New("invalid snapshot ancestry")
		}
		current = commit.ParentHashes[0]
	}
	return result, nil
}

// Verify checks the selected commit, tree/blob hashes, manifest checksums,
// coverage and the complete snapshot schema. It does not contact GitHub.
func (s *Store) Verify(ctx context.Context, revision string) error {
	_, err := s.Load(ctx, revision)
	return err
}

func effectiveTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}
