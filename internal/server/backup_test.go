package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A future last_backup timestamp (clock skew or a bad write) must not read as
// "ok": the age is clamped to zero and the state flagged stale.
func TestReadBackupStatusFutureTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"last_backup":"` + future + `","ok":true,"size_bytes":1048576}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readBackupStatus(path)
	if st.State != "stale" {
		t.Errorf("state = %q, want stale for a future timestamp", st.State)
	}
	if st.AgeHours != 0 {
		t.Errorf("AgeHours = %d, want 0 (clamped) for a future timestamp", st.AgeHours)
	}
}

// A recent successful backup reads as ok with a sane age.
func TestReadBackupStatusOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	recent := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"last_backup":"` + recent + `","ok":true,"size_bytes":2097152}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readBackupStatus(path)
	if st.State != "ok" {
		t.Errorf("state = %q, want ok", st.State)
	}
	if st.AgeHours < 0 {
		t.Errorf("AgeHours = %d, want >= 0", st.AgeHours)
	}
}
