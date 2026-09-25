//go:build windows

package backup

// Windows has no directory-fsync equivalent exposed by os.File.Sync. The
// production target is Linux, where syncDirectory is strict; keep Windows
// development builds functional after os.Rename has completed.
// syncDirectory is a no-op on Windows, where directory handles cannot be synced
// through the portable os.File API used by the Unix implementation.
func syncDirectory(string) error { return nil }
