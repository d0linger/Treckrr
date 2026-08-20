package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
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

func TestHumanAgeDE(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero", time.Time{}, "—"},
		{"future clamps to now", now.Add(48 * time.Hour), "gerade eben"},
		// Offsets carry a half-unit margin so the sub-second test runtime can't flip
		// the truncated integer.
		{"minutes", now.Add(-(5*time.Minute + 30*time.Second)), "vor 5 Min."},
		{"hours", now.Add(-(3*time.Hour + 30*time.Minute)), "vor 3 Std."},
		{"one day", now.Add(-25 * time.Hour), "vor 1 Tag"},
		{"days", now.Add(-(2*24*time.Hour + 12*time.Hour)), "vor 2 Tagen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := humanAgeDE(c.t); got != c.want {
				t.Errorf("humanAgeDE = %q, want %q", got, c.want)
			}
		})
	}
}

// backupHealth maps the classified status to the dashboard tile's tone; verify the
// enabled ok/stale/failed cases and the "not activated" fallback.
func TestBackupHealthTone(t *testing.T) {
	writeStatus := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "status.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	enabled := backup.New(backup.Options{EncKey: "not-a-real-secret"}, nil)
	recent := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name, body, wantTone string
	}{
		{"ok", `{"last_backup":"` + recent + `","ok":true,"size_bytes":1048576}`, "ok"},
		{"stale", `{"last_backup":"` + old + `","ok":true,"size_bytes":1048576}`, "warn"},
		{"failed", `{"last_backup":"` + recent + `","ok":false}`, "bad"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{BackupStatusFile: writeStatus(t, c.body)}, backup: enabled}
			if got := s.backupHealth().Tone; got != c.wantTone {
				t.Errorf("tone = %q, want %q", got, c.wantTone)
			}
		})
	}

	t.Run("not activated", func(t *testing.T) {
		s := &Server{cfg: &config.Config{}} // no backup service
		v := s.backupHealth()
		if v.Tone != "warn" || v.Title != "Backups nicht aktiviert" {
			t.Errorf("got tone=%q title=%q, want warn / not-activated", v.Tone, v.Title)
		}
	})
}
