//go:build windows

package issuearchive

import (
	"errors"
	"os"
)

// Windows FlushFileBuffers requires a write-capable handle, while go-git marks
// loose objects read-only. Under the archive writer lock, temporarily enable the
// write attribute, flush without writing any content, and restore the attribute.
func syncObjectFile(path string) (err error) {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if err := os.Chmod(path, mode|0200); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Chmod(path, mode)) }()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
