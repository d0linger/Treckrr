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
	"strings"
	"time"

	"treckrr/internal/backup"
	"treckrr/internal/models"
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
	S3            string
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
	if d.S3OK != nil {
		if *d.S3OK {
			st.S3 = "ok"
		} else {
			st.S3 = "fehlgeschlagen"
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
	s3on := st.Enabled && s.backup.S3Enabled()
	data["S3Enabled"] = s3on
	if st.Enabled {
		if files, err := s.backup.List(); err == nil {
			data["Files"] = toFileViews(files)
		}
	}
	if s3on {
		data["S3Bucket"] = s.cfg.S3Bucket
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if objs, err := s.backup.S3List(ctx); err == nil {
			data["S3Files"] = toFileViews(objs)
		} else {
			data["S3Error"] = sanitizeLog(err.Error())
		}
	}
	if st.Enabled {
		if bs, err := s.store.GetBackupSettings(r.Context()); err == nil {
			data["Settings"] = bs
			data["VolumeCronDesc"] = backup.DescribeCron(bs.VolumeCron)
			data["S3CronDesc"] = backup.DescribeCron(bs.S3Cron)
		}
		nv, ns := s.backup.NextRuns(r.Context())
		data["NextVolume"] = fmtNext(nv)
		data["NextS3"] = fmtNext(ns)
	}
	s.render(w, r, "backup", data)
}

// handleBackupRunScheduled runs a volume backup now (the panel's manual trigger).
func (s *Server) handleBackupRunScheduled(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil || !s.backup.Enabled() {
		s.setFlash(w, r, "error", "Backups sind nicht konfiguriert.")
		redirect(w, r, "/admin/backup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.backup.ManualVolume(ctx); err != nil {
		log.Printf("manual backup failed: %v", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Backup fehlgeschlagen.")
	} else {
		s.audit(r, "backup_run", "backup", 0, "Volume-Backup manuell erstellt")
		s.setFlash(w, r, "success", "Volume-Backup erstellt.")
	}
	redirect(w, r, "/admin/backup")
}

// handleBackupSettings persists the GUI-edited backup schedule.
func (s *Server) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	bs := models.BackupSettings{
		VolumeCron: strings.TrimSpace(r.FormValue("volume_cron")),
		VolumeKeep: clampAtoi(r.FormValue("volume_keep"), 7),
		S3Cron:     strings.TrimSpace(r.FormValue("s3_cron")),
		S3Keep:     clampAtoi(r.FormValue("s3_keep"), 0),
	}
	if !backup.ValidCron(bs.VolumeCron) || !backup.ValidCron(bs.S3Cron) {
		s.setFlash(w, r, "error", "Ungültiger Cron-Ausdruck (Format: Minute Stunde Tag Monat Wochentag).")
		redirect(w, r, "/admin/backup")
		return
	}
	if err := s.store.UpdateBackupSettings(r.Context(), bs); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.audit(r, "backup_settings", "backup", 0,
			fmt.Sprintf("Volume %q/behalte %d · S3 %q/behalte %d",
				bs.VolumeCron, bs.VolumeKeep, bs.S3Cron, bs.S3Keep))
		s.setFlash(w, r, "success", "Backup-Zeitplan gespeichert.")
	}
	redirect(w, r, "/admin/backup")
}

// fmtNext renders a next-run time for the panel ("—" when unknown/disabled).
func fmtNext(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("02.01.2006 15:04")
}

// clampAtoi parses a non-negative int form value, falling back to def and
// clamping to a sane ceiling.
func clampAtoi(v string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return def
	}
	if n > 24*365 {
		n = 24 * 365
	}
	return n
}

func toFileViews(files []backup.BackupFile) []backupFileView {
	views := make([]backupFileView, 0, len(files))
	for _, f := range files {
		views = append(views, backupFileView{Name: f.Name, Size: humanSize(f.Size), ModTime: f.ModTime})
	}
	return views
}

// handleBackupS3Test checks the bucket is reachable and reports success/failure.
func (s *Server) handleBackupS3Test(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil || !s.backup.S3Enabled() {
		s.setFlash(w, r, "error", "S3 ist nicht konfiguriert (S3_* setzen).")
		redirect(w, r, "/admin/backup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.backup.S3Test(ctx); err != nil {
		s.setFlash(w, r, "error", "S3-Verbindung fehlgeschlagen: "+sanitizeLog(err.Error()))
	} else {
		s.setFlash(w, r, "success", "S3-Verbindung ok — Bucket erreichbar.")
	}
	redirect(w, r, "/admin/backup")
}

// handleBackupS3File streams a stored dump from the bucket (still encrypted).
func (s *Server) handleBackupS3File(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil || !s.backup.S3Enabled() {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	data, err := s.backup.S3Get(ctx, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.audit(r, "backup_download", "backup", 0, "S3 · "+name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// backupFileView is one row in the panel's stored-backups list.
type backupFileView struct {
	Name    string
	Size    string
	ModTime time.Time
}

// handleBackupFile streams a stored (scheduled) encrypted dump for download.
func (s *Server) handleBackupFile(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil || !s.backup.Enabled() {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	data, err := s.backup.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.audit(r, "backup_download", "backup", 0, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handleBackupValidate decrypts an uploaded backup with the re-entered key and
// validates the archive — no DB change. It is the safety preview before restore.
func (s *Server) handleBackupValidate(w http.ResponseWriter, r *http.Request) {
	s.backupUpload(w, r, false)
}

// handleBackupRestore validates an uploaded backup and, only after the operator
// re-enters the key and types RESTORE, overwrites the live database.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	s.backupUpload(w, r, true)
}

// backupUpload is the shared upload+decrypt+validate flow. When doRestore is
// true and the typed confirmation matches, it restores over the live DB.
func (s *Server) backupUpload(w http.ResponseWriter, r *http.Request, doRestore bool) {
	if s.backup == nil || !s.backup.Enabled() {
		s.setFlash(w, r, "error", "Backups sind nicht konfiguriert (BACKUP_ENCRYPTION_KEY setzen).")
		redirect(w, r, "/admin/backup")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.setFlash(w, r, "error", "Upload fehlgeschlagen (Datei zu groß?).")
		redirect(w, r, "/admin/backup")
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		s.setFlash(w, r, "error", "Bitte den Backup-Schlüssel eingeben.")
		redirect(w, r, "/admin/backup")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.setFlash(w, r, "error", "Bitte eine Backup-Datei (.dump.enc) wählen.")
		redirect(w, r, "/admin/backup")
		return
	}
	defer file.Close()
	enc, err := io.ReadAll(file)
	if err != nil {
		s.setFlash(w, r, "error", "Datei konnte nicht gelesen werden.")
		redirect(w, r, "/admin/backup")
		return
	}
	// The key is re-entered here and used only transiently — never stored.
	raw, err := backup.DecryptWith(enc, key)
	if err != nil {
		// A failed decryption is security-relevant (wrong key / brute force): audit it.
		action := "backup_validate_failed"
		if doRestore {
			action = "backup_restore_failed"
		}
		s.audit(r, action, "backup", 0, "Entschlüsselung fehlgeschlagen (falscher Schlüssel oder beschädigte Datei)")
		s.setFlash(w, r, "error", "Entschlüsselung fehlgeschlagen — falscher Schlüssel oder beschädigte Datei.")
		redirect(w, r, "/admin/backup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	objects, err := s.backup.ValidateArchive(ctx, raw)
	if err != nil {
		s.setFlash(w, r, "error", "Kein gültiges Backup-Archiv: "+sanitizeLog(err.Error()))
		redirect(w, r, "/admin/backup")
		return
	}
	if !doRestore {
		s.audit(r, "backup_validate", "backup", 0, fmt.Sprintf("Upload validiert · %d Objekte", objects))
		s.setFlash(w, r, "success",
			fmt.Sprintf("Backup gültig ✓ — entschlüsselt, %d Objekte im Archiv. Zum Einspielen unten RESTORE eintippen.", objects))
		redirect(w, r, "/admin/backup")
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != "RESTORE" {
		s.setFlash(w, r, "error", "Zum Ausführen bitte RESTORE eintippen (Groß­schreibung beachten).")
		redirect(w, r, "/admin/backup")
		return
	}
	if err := s.backup.RestoreRaw(ctx, raw, s.cfg.DatabaseURL); err != nil {
		log.Printf("restore failed: %v", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Wiederherstellung fehlgeschlagen.")
		redirect(w, r, "/admin/backup")
		return
	}
	s.audit(r, "backup_restore", "backup", 0, fmt.Sprintf("%d Objekte aus Upload wiederhergestellt", objects))
	s.setFlash(w, r, "success", "Wiederherstellung abgeschlossen. Bitte prüfen und ggf. neu anmelden.")
	redirect(w, r, "/admin/backup")
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
