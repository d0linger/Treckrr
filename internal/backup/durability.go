package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

// durableRename makes the directory-entry change durable before success is
// reported. Syncing only the file protects its bytes, not the rename itself.
func durableRename(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	newDir := filepath.Dir(newPath)
	if err := syncDirectory(newDir); err != nil {
		return fmt.Errorf("sync destination directory after rename: %w", err)
	}
	oldDir := filepath.Dir(oldPath)
	if oldDir != newDir {
		if err := syncDirectory(oldDir); err != nil {
			return fmt.Errorf("sync source directory after rename: %w", err)
		}
	}
	return nil
}
