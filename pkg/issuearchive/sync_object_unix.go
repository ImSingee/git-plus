//go:build !windows

package issuearchive

import (
	"errors"
	"os"
)

func syncObjectFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
