package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"treckrr/internal/backup"
)

// backupMaxAge is how old the last successful backup may be before the panel
// flags it stale (a little over a daily cadence).
const backupMaxAge = 26 * time.Hour

// backupStatus is the admin-panel view of the backup status.json.
type backupStatus struct {
	Enabled       bool   // a BACKUP_ENCRYPTION_KEY is configured (on-demand available)
	Configured    bool   // a status.json exists (scheduled backups have run)
	State         string // "ok" | "stale" | "failed" | "none"
	LastBackup    time.Time
	AgeHours      int
	SizeLabel     string
	Offhost       string
	Encrypted     bool
	SchemaVersion string
	RestoreTested time.Time
}

// readBackupStatus loads and classifies the backup status file. Any problem
// (path empty, file missing, malformed) is reported as an unconfigured state
// rather than an error — the panel is informational.
func readBackupStatus(path string) backupStatus {
	if path == "" {
		return backupStatus{State: "none"}
	}
	// path is operator configuration (cfg.BackupStatusFile), never user input.
	// Read it through an os.Root scoped to its directory so the access can't
	// traverse outside that directory (satisfies gosec G304 without a blanket
	// suppression).
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return backupStatus{State: "none"}
	}
	defer root.Close()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return backupStatus{State: "none"}
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return backupStatus{State: "none"}
	}
	var d backup.Status
	if err := json.Unmarshal(b, &d); err != nil {
		return backupStatus{State: "none"}
	}
	st := backupStatus{
		Configured:    true,
		LastBackup:    d.LastBackup,
		SizeLabel:     humanSize(d.SizeBytes),
		Offhost:       "—",
		Encrypted:     d.Encrypted,
		SchemaVersion: d.SchemaVersion,
		RestoreTested: d.RestoreTested,
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
	st := readBackupStatus(s.cfg.BackupStatusFile)
	st.Enabled = s.backup != nil && s.backup.Enabled()
	data := s.newPage(w, r, "Backup", "backup")
	data["Backup"] = st
	s.render(w, r, "backup", data)
}

// handleBackupRun makes an encrypted dump on demand and streams it to the browser
// as a download — the operator stores it wherever they like. The DB is never
// touched destructively here.
func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil || !s.backup.Enabled() {
		s.setFlash(w, r, "error", "Backups sind nicht konfiguriert (BACKUP_ENCRYPTION_KEY setzen).")
		redirect(w, r, "/admin/backup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	data, filename, err := s.backup.CreateEncrypted(ctx)
	if err != nil {
		log.Printf("on-demand backup failed: %v", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Backup fehlgeschlagen.")
		redirect(w, r, "/admin/backup")
		return
	}
	s.audit(r, "backup_download", "backup", 0, filename+" · "+humanSize(int64(len(data))))
	// The default server WriteTimeout is short; extend it for this one response so
	// a larger dump can finish streaming.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Minute))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
