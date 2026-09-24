//go:build windows

package backup

// Windows has no directory-fsync equivalent exposed by os.File.Sync. The
// production target is Linux, where syncDirectory is strict; keep Windows
// development builds functional after os.Rename has completed.
func syncDirectory(string) error { return nil }
