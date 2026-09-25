//go:build !windows

package backup

import "os"

// syncDirectory persists directory-entry changes on Unix systems.
func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- operator-configured backup/status parent directory
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
