package issuearchive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AcquireLock acquires a nonblocking, cross-process exclusive advisory lock on
// the exact file path. It creates the parent directory if necessary. Contention
// returns ErrBusy. The returned function is idempotent; the OS also releases the
// lock when the process exits. The persistent lock file must not be removed or
// replaced while the application is running, as that would split the lock domain.
// Callers may use a separate <archive>.sync.lock to serialize archive/index work.
func AcquireLock(path string) (func(), error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("lock path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("lock path must be a regular file, not a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open archive lock: %w", err)
	}
	if err := acquireFileLock(file); err != nil {
		file.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { releaseFileLock(file); _ = file.Close() }) }, nil
}

func lock(path string) (func(), error) {
	return AcquireLock(filepath.Join(path, ".issuearchive.lock"))
}
