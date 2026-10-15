//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package issuearchive

import (
	"errors"
	"os"
)

func acquireFileLock(*os.File) error {
	return errors.New("issue archive advisory locks are unsupported on this operating system")
}
func releaseFileLock(*os.File) {}
