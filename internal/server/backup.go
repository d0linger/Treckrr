package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// backupMaxAge is how old the last successful backup may be before the panel
// flags it stale (a little over a daily cadence).
const backupMaxAge = 26 * time.Hour

// backupStatus is the admin-panel view of the backup service's status.json.
type backupStatus struct {
	Configured bool
	State      string // "ok" | "stale" | "failed" | "none"
	LastBackup time.Time
	AgeHours   int
	SizeLabel  string
	Offhost    string
}

// diskStatus is the on-disk shape written by the backup container.
type diskStatus struct {
	LastBackup time.Time `json:"last_backup"`
	OK         bool      `json:"ok"`
	SizeBytes  int64     `json:"size_bytes"`
	OffhostOK  *bool     `json:"offhost_ok"`
}

// readBackupStatus loads and classifies the backup status file. Any problem
// (path empty, file missing, malformed) is reported as an unconfigured state
// rather than an error — the panel is informational.
func readBackupStatus(path string) backupStatus {
	if path == "" {
		return backupStatus{State: "none"}
	}
	// path is operator configuration (cfg.BackupStatusFile), never user input.
	b, err := os.ReadFile(path) //nosec G304 -- operator-configured status file path
	if err != nil {
		return backupStatus{State: "none"}
	}
	var d diskStatus
	if err := json.Unmarshal(b, &d); err != nil {
		return backupStatus{State: "none"}
	}
	st := backupStatus{
		Configured: true,
		LastBackup: d.LastBackup,
		SizeLabel:  humanSize(d.SizeBytes),
		Offhost:    "—",
	}
	if d.OffhostOK != nil {
		if *d.OffhostOK {
			st.Offhost = "ok"
		} else {
			st.Offhost = "fehlgeschlagen"
		}
	}
	age := time.Since(d.LastBackup)
	// A future timestamp (clock skew or a bad write) is not trustworthy: clamp the
	// reported age to zero and treat it as stale rather than silently "ok".
	future := age < 0
	if future {
		age = 0
	}
	st.AgeHours = int(age.Hours())
	switch {
	case !d.OK:
		st.State = "failed"
	case d.LastBackup.IsZero() || future || age > backupMaxAge:
		st.State = "stale"
	default:
		st.State = "ok"
	}
	return st
}

func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "—"
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// handleBackupStatus renders the admin Backup panel from status.json.
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	data := s.newPage(w, r, "Backup", "backup")
	data["Backup"] = readBackupStatus(s.cfg.BackupStatusFile)
	s.render(w, r, "backup", data)
}
