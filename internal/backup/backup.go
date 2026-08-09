// Package backup creates encrypted PostgreSQL dumps (on demand and on a timer),
// and restores/verifies them. Every dump is AES-256-GCM encrypted at rest with a
// key held separately from the app's session/data secrets.
package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/robfig/cron/v3"
)

// parseCron parses a standard 5-field cron; empty/invalid yields ok=false.
func parseCron(expr string) (cron.Schedule, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, false
	}
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, false
	}
	return s, true
}

// ValidCron reports whether expr is empty (= off) or a valid cron.
func ValidCron(expr string) bool {
	if strings.TrimSpace(expr) == "" {
		return true
	}
	_, ok := parseCron(expr)
	return ok
}

// cronDue reports whether a run scheduled by expr is due now, given the last run.
func cronDue(expr string, last, now time.Time) bool {
	s, ok := parseCron(expr)
	if !ok {
		return false
	}
	return !now.Before(s.Next(last))
}

// cronNext returns the next scheduled time after from (zero if off/invalid).
func cronNext(expr string, from time.Time) time.Time {
	s, ok := parseCron(expr)
	if !ok {
		return time.Time{}
	}
	return s.Next(from)
}

var cronDOW = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}

// DescribeCron renders a standard 5-field cron in plain German for the panel.
// Unknown-but-valid expressions fall back to showing the raw expression.
func DescribeCron(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "Ausgeschaltet (kein Zeitplan)."
	}
	p := strings.Fields(expr)
	if len(p) != 5 {
		return "Ungültig: erwartet 5 Felder (Minute Stunde Tag Monat Wochentag)."
	}
	if _, ok := parseCron(expr); !ok {
		return "Ungültiger Cron-Ausdruck."
	}
	mi, ho, dom, mon, dow := p[0], p[1], p[2], p[3], p[4]
	num := func(s string) (int, bool) { n, err := strconv.Atoi(s); return n, err == nil }
	star := func(a, b, c string) bool { return a == "*" && b == "*" && c == "*" }
	if n, ok := everyN(mi); ok && ho == "*" && star(dom, mon, dow) {
		return fmt.Sprintf("Alle %d Minuten.", n)
	}
	if mi == "*" && ho == "*" && star(dom, mon, dow) {
		return "Jede Minute."
	}
	if mi == "0" && ho == "*" && star(dom, mon, dow) {
		return "Stündlich (zur vollen Stunde)."
	}
	if _, ok := num(mi); ok && ho == "*" && star(dom, mon, dow) {
		return "Stündlich um Minute " + mi + "."
	}
	if n, ok := everyN(ho); ok {
		if _, ok2 := num(mi); ok2 && star(dom, mon, dow) {
			return fmt.Sprintf("Alle %d Stunden (um Minute %s).", n, mi)
		}
	}
	if m, ok := num(mi); ok {
		if h, ok2 := num(ho); ok2 && mon == "*" {
			t := fmt.Sprintf("%02d:%02d Uhr", h, m)
			switch {
			case dom == "*" && dow == "*":
				return "Täglich um " + t + "."
			case dom == "*" && dow != "*":
				if d, ok3 := num(dow); ok3 {
					return "Jeden " + cronDOW[d%7] + " um " + t + "."
				}
			case dom != "*" && dow == "*":
				return "Monatlich am " + dom + ". um " + t + "."
			}
		}
	}
	return "Cron: " + expr + " (Standardausdruck)."
}

func everyN(field string) (int, bool) {
	if !strings.HasPrefix(field, "*/") {
		return 0, false
	}
	n, err := strconv.Atoi(field[2:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// magic prefixes every encrypted dump so a wrong/plain file is rejected early.
const magic = "TRKBK1"

// Status is the on-disk shape written after each scheduled backup and read by the
// admin panel. Timestamps are zero when the event never happened.
type Status struct {
	LastBackup    time.Time `json:"last_backup"`
	OK            bool      `json:"ok"`
	SizeBytes     int64     `json:"size_bytes"`
	OffhostOK     *bool     `json:"offhost_ok,omitempty"`
	S3OK          *bool     `json:"s3_ok,omitempty"`
	LastS3        time.Time `json:"last_s3,omitempty"`
	Encrypted     bool      `json:"encrypted"`
	SchemaVersion string    `json:"schema_version,omitempty"`
	RestoreTested time.Time `json:"restore_tested,omitempty"`
}

// Settings is the runtime backup schedule, editable in the GUI. An empty cron
// disables that destination.
type Settings struct {
	VolumeCron string
	VolumeKeep int
	S3Cron     string
	S3Keep     int
}

// S3Options configures an optional S3-compatible off-box destination.
type S3Options struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string
	UseSSL    bool
}

func (o S3Options) enabled() bool { return o.Endpoint != "" && o.Bucket != "" }

// Options configures a Service. EncKey empty means backups are disabled.
type Options struct {
	DatabaseURL string
	EncKey      string
	Dir         string
	StatusFile  string
	Keep        int
	S3          S3Options
	// SettingsFn returns the current GUI-editable schedule. When nil the service
	// falls back to a fixed default cron and Keep. Called each scheduler tick so
	// GUI edits apply without a restart.
	SettingsFn func(context.Context) Settings
}

// BackupFile describes one stored encrypted dump for the admin panel list.
type BackupFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Service runs and restores encrypted backups.
type Service struct {
	opt Options
	db  *sql.DB
	key []byte // 32-byte AES-256 key, nil when disabled

	mu         sync.Mutex // serializes status.json read-modify-write
	volRetryAt time.Time  // earliest next volume attempt after a failure (tick goroutine only)
	s3RetryAt  time.Time  // earliest next S3 attempt after a failure (tick goroutine only)
}

// statusRetryBackoff is how long the scheduler waits before retrying a failed
// destination, so a persistent failure (DB down, disk full) doesn't turn into a
// heavy pg_dump loop every minute.
const statusRetryBackoff = 15 * time.Minute

// updateStatus applies mutate to status.json under the lock, re-reading first so
// a concurrent run's fields (e.g. the other destination's timestamps) are
// preserved instead of clobbered by a stale read-modify-write.
func (s *Service) updateStatus(mutate func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.readStatus()
	mutate(&st)
	s.writeStatus(st)
}

// New builds a Service. When opt.EncKey is empty the service is disabled and all
// operations return ErrDisabled.
func New(opt Options, db *sql.DB) *Service {
	s := &Service{opt: opt, db: db}
	if opt.EncKey != "" {
		k := sha256.Sum256([]byte(opt.EncKey))
		s.key = k[:]
	}
	return s
}

// Enabled reports whether a backup key is configured.
func (s *Service) Enabled() bool { return s.key != nil }

// ErrDisabled is returned when no BACKUP_ENCRYPTION_KEY is configured.
var ErrDisabled = fmt.Errorf("backups are not configured (set BACKUP_ENCRYPTION_KEY)")

// Filename is the name of a dump taken at t, e.g. treckrr-2026-08-01-030000.dump.enc.
func Filename(t time.Time) string {
	return "treckrr-" + t.Format("2006-01-02-150405") + ".dump.enc"
}

// encrypt seals plaintext with AES-256-GCM: magic || nonce || ciphertext(+tag).
// One-shot (the dump fits comfortably in memory); the GCM tag covers the whole
// message, so any truncation or tampering fails on decrypt.
func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte(magic), nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt, rejecting a bad magic (wrong file) or bad key/tag.
func decrypt(enc, key []byte) ([]byte, error) {
	if len(enc) < len(magic) || string(enc[:len(magic)]) != magic {
		return nil, fmt.Errorf("not a Treckrr backup (bad header)")
	}
	enc = enc[len(magic):]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(enc) < ns {
		return nil, fmt.Errorf("backup truncated")
	}
	nonce, ct := enc[:ns], enc[ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed (wrong key or corrupt backup): %w", err)
	}
	return pt, nil
}

// dump runs pg_dump in PostgreSQL's custom format (compressed, restorable with
// pg_restore) against the configured database and returns the raw archive bytes.
func (s *Service) dump(ctx context.Context) ([]byte, error) {
	// The connection URL comes from trusted deployment config (DATABASE_URL),
	// not user input, and libpq offers no argv-free way to pass a full URI.
	cmd := exec.CommandContext(ctx, "pg_dump", // #nosec G204
		"--format=custom", "--no-owner", "--no-privileges", s.opt.DatabaseURL)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg_dump: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// SchemaVersion returns the latest applied migration name, embedded in backups so
// a restore can be checked against the running binary's schema.
func (s *Service) SchemaVersion(ctx context.Context) string {
	var name string
	if s.db == nil {
		return ""
	}
	_ = s.db.QueryRowContext(ctx,
		`SELECT name FROM schema_migrations ORDER BY name DESC LIMIT 1`).Scan(&name)
	return name
}

// CreateEncrypted produces one encrypted dump in memory and its filename — the
// path shared by the on-demand download and the scheduled writer.
func (s *Service) CreateEncrypted(ctx context.Context) (data []byte, filename string, err error) {
	if !s.Enabled() {
		return nil, "", ErrDisabled
	}
	raw, err := s.dump(ctx)
	if err != nil {
		return nil, "", err
	}
	enc, err := encrypt(raw, s.key)
	if err != nil {
		return nil, "", err
	}
	return enc, Filename(time.Now()), nil
}

// currentSettings returns the live GUI schedule, or the Options fallback.
func (s *Service) currentSettings(ctx context.Context) Settings {
	if s.opt.SettingsFn != nil {
		return s.opt.SettingsFn(ctx)
	}
	return Settings{VolumeCron: "0 3 * * *", VolumeKeep: s.opt.Keep, S3Cron: "0 4 * * *"}
}

// readStatus loads status.json (best-effort) so a run can update its own fields
// without clobbering the other destination's timestamps.
func (s *Service) readStatus() Status {
	var st Status
	if s.opt.StatusFile == "" {
		return st
	}
	// StatusFile is operator-configured deployment config, not user input.
	b, err := os.ReadFile(s.opt.StatusFile) // #nosec G304
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// RunScheduled makes a volume backup and (if configured) mirrors it to S3 — the
// CLI `treckrr backup`. The scheduler uses runVolume/runS3Mirror independently.
func (s *Service) RunScheduled(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	set := s.currentSettings(ctx)
	if err := s.runVolume(ctx, set.VolumeKeep); err != nil {
		return err
	}
	if s.S3Enabled() {
		_ = s.runS3Mirror(ctx, set.S3Keep)
	}
	return nil
}

// runVolume writes one encrypted dump to the volume, prunes to keep, verifies it
// restores, and updates status — without touching S3.
func (s *Service) runVolume(ctx context.Context, keep int) error {
	enc, name, err := s.CreateEncrypted(ctx)
	if err != nil {
		s.updateStatus(func(st *Status) { st.OK = false })
		return err
	}
	if err := os.MkdirAll(s.opt.Dir, 0o750); err != nil {
		s.updateStatus(func(st *Status) { st.OK = false })
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.opt.Dir, name), enc); err != nil {
		s.updateStatus(func(st *Status) { st.OK = false })
		return err
	}
	s.prune(keep)
	// A backup you have never restored is not a backup: confirm it decrypts and
	// is a well-formed archive before recording success.
	restoreTested := s.verifyRestorable(ctx, enc) == nil
	schema := s.SchemaVersion(ctx)
	now := time.Now()
	s.updateStatus(func(st *Status) {
		st.Encrypted = true
		st.SchemaVersion = schema
		st.LastBackup = now
		st.OK = true
		st.SizeBytes = int64(len(enc))
		if restoreTested {
			st.RestoreTested = now
		}
	})
	return nil
}

// runS3Mirror uploads the newest volume dump not already in the bucket, prunes S3
// to s3keep, and records the S3 status/time.
func (s *Service) runS3Mirror(ctx context.Context, s3keep int) error {
	if !s.S3Enabled() {
		return nil
	}
	files, err := s.List()
	if err != nil {
		return err
	}
	var name string
	var data []byte
	if len(files) > 0 {
		newest := files[0].Name
		remote, _ := s.S3List(ctx)
		for _, r := range remote {
			if r.Name == newest { // already mirrored — just refresh the timer
				now := time.Now()
				s.updateStatus(func(st *Status) { ok := true; st.S3OK, st.LastS3 = &ok, now })
				return nil
			}
		}
		name = newest
		if data, err = s.Open(newest); err != nil {
			return err
		}
	} else {
		// No local volume dump (e.g. the volume schedule is off) — create a fresh
		// one just for S3 so the off-box copy still happens rather than silently
		// never running.
		if data, name, err = s.CreateEncrypted(ctx); err != nil {
			return err
		}
	}
	uerr := s.uploadS3(ctx, name, data)
	ok := uerr == nil
	now := time.Now()
	s.updateStatus(func(st *Status) { st.S3OK, st.LastS3 = &ok, now })
	if ok {
		s.pruneS3(ctx, s3keep)
	}
	return uerr
}

// prune keeps only the newest keep dumps in the volume backup dir.
func (s *Service) prune(keep int) {
	if keep <= 0 {
		return
	}
	entries, err := filepath.Glob(filepath.Join(s.opt.Dir, "treckrr-*.dump.enc"))
	if err != nil || len(entries) <= keep {
		return
	}
	sort.Strings(entries) // timestamped names sort chronologically
	for _, old := range entries[:len(entries)-keep] {
		_ = os.Remove(old)
	}
}

// ValidateArchive writes the raw (decrypted) dump to a temp file and lists its
// table of contents via pg_restore --list — proving it is a well-formed archive
// — and returns the object count. A corrupt/truncated dump fails here.
func (s *Service) ValidateArchive(ctx context.Context, raw []byte) (int, error) {
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	// tmp is an internal temp file we just wrote, not user input.
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", tmp) // #nosec G204
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("not a valid archive: %s", strings.TrimSpace(errBuf.String()))
	}
	n := 0
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, ";") {
			n++
		}
	}
	return n, nil
}

// RestoreRaw restores decrypted dump bytes into targetURL, dropping and
// recreating objects (--clean). Destructive — callers must confirm.
func (s *Service) RestoreRaw(ctx context.Context, raw []byte, targetURL string) error {
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	// targetURL comes from trusted deployment config, not user input.
	cmd := exec.CommandContext(ctx, "pg_restore", // #nosec G204
		"--clean", "--if-exists", "--no-owner", "--dbname="+targetURL, tmp)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// verifyRestorable is the automatic post-write drill: decrypt with the service
// key and validate the archive.
func (s *Service) verifyRestorable(ctx context.Context, enc []byte) error {
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return err
	}
	_, err = s.ValidateArchive(ctx, raw)
	return err
}

// DecryptWith decrypts using an operator-supplied key string — used by the GUI
// restore, where the key is re-entered rather than taken from the environment.
func DecryptWith(enc []byte, keySecret string) ([]byte, error) {
	k := sha256.Sum256([]byte(keySecret))
	return decrypt(enc, k[:])
}

// Restore decrypts a backup file (with the service key) and restores it. CLI use.
func (s *Service) Restore(ctx context.Context, encFile, targetURL string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	// encFile is an operator-supplied path from the restore CLI, not user input.
	enc, err := os.ReadFile(encFile) // #nosec G304
	if err != nil {
		return err
	}
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return err
	}
	return s.RestoreRaw(ctx, raw, targetURL)
}

// TestReport summarizes a test-restore.
type TestReport struct {
	Objects       int
	SchemaVersion string
}

// TestRestore validates a backup file without touching the live DB. CLI use.
func (s *Service) TestRestore(ctx context.Context, encFile string) (TestReport, error) {
	var rep TestReport
	if !s.Enabled() {
		return rep, ErrDisabled
	}
	// encFile is an operator-supplied path from the restore CLI, not user input.
	enc, err := os.ReadFile(encFile) // #nosec G304
	if err != nil {
		return rep, err
	}
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return rep, err
	}
	n, err := s.ValidateArchive(ctx, raw)
	if err != nil {
		return rep, err
	}
	rep.Objects = n
	rep.SchemaVersion = s.SchemaVersion(ctx)
	return rep, nil
}

// List returns the stored encrypted dumps, newest first.
func (s *Service) List() ([]BackupFile, error) {
	paths, err := filepath.Glob(filepath.Join(s.opt.Dir, "treckrr-*.dump.enc"))
	if err != nil {
		return nil, err
	}
	out := make([]BackupFile, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, BackupFile{Name: filepath.Base(p), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// validName guards a requested filename against traversal and enforces the
// backup naming scheme.
func validName(name string) bool {
	return name == filepath.Base(name) &&
		strings.HasPrefix(name, "treckrr-") && strings.HasSuffix(name, ".dump.enc")
}

// Open returns the bytes of a stored encrypted dump for download.
func (s *Service) Open(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid backup name")
	}
	root, err := os.OpenRoot(s.opt.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Loop polls once a minute and runs the volume and S3 backups independently when
// each is due (overdue on boot). Reading the schedule from the DB each tick means
// GUI edits apply without a restart, and a mere restart no longer forces a backup.
func (s *Service) Loop(ctx context.Context, logf func(string, ...any)) {
	if !s.Enabled() {
		return
	}
	s.tick(ctx, logf)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx, logf)
		}
	}
}

func (s *Service) tick(ctx context.Context, logf func(string, ...any)) {
	set := s.currentSettings(ctx)
	st := s.readStatus()
	now := time.Now()
	// The `now.After(retryAt)` guard bounds a persistently failing destination to
	// one attempt per statusRetryBackoff instead of a heavy pg_dump every minute
	// (LastBackup/LastS3 only advance on success, so cronDue stays true meanwhile).
	if cronDue(set.VolumeCron, st.LastBackup, now) && now.After(s.volRetryAt) {
		c, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if err := s.runVolume(c, set.VolumeKeep); err != nil {
			s.volRetryAt = now.Add(statusRetryBackoff)
			logf("volume backup failed: %v", err)
		} else {
			s.volRetryAt = time.Time{}
			logf("volume backup written to %s", s.opt.Dir)
		}
		cancel()
	}
	if s.S3Enabled() {
		st = s.readStatus()
		if cronDue(set.S3Cron, st.LastS3, now) && now.After(s.s3RetryAt) {
			c, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if err := s.runS3Mirror(c, set.S3Keep); err != nil {
				s.s3RetryAt = now.Add(statusRetryBackoff)
				logf("s3 mirror failed: %v", err)
			} else {
				s.s3RetryAt = time.Time{}
				logf("s3 mirror updated")
			}
			cancel()
		}
	}
}

// ManualVolume runs a volume backup now (the panel's scheduled-backup trigger).
func (s *Service) ManualVolume(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return s.runVolume(ctx, s.currentSettings(ctx).VolumeKeep)
}

// ManualS3 creates a fresh encrypted dump and uploads it straight to S3 now —
// the panel's "Jetzt in S3 sichern". Independent of the volume backups (works
// even when the volume schedule is off and no local dump exists) and never
// prunes. Dumps in-memory, so it is safe under a read-only root filesystem.
func (s *Service) ManualS3(ctx context.Context) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	if !s.S3Enabled() {
		return "", fmt.Errorf("S3 ist nicht konfiguriert")
	}
	enc, name, err := s.CreateEncrypted(ctx)
	if err != nil {
		return "", err
	}
	uerr := s.uploadS3(ctx, name, enc)
	ok := uerr == nil
	now := time.Now()
	s.updateStatus(func(st *Status) { st.S3OK, st.LastS3 = &ok, now })
	if uerr != nil {
		return "", uerr
	}
	return name, nil
}

// NextRuns returns when the next volume and S3 backups are due (zero = unknown or
// disabled), for the panel.
func (s *Service) NextRuns(ctx context.Context) (nextVolume, nextS3 time.Time) {
	set := s.currentSettings(ctx)
	now := time.Now()
	nextVolume = cronNext(set.VolumeCron, now)
	if s.S3Enabled() {
		nextS3 = cronNext(set.S3Cron, now)
	}
	return
}

// S3Enabled reports whether an off-box S3 destination is configured.
func (s *Service) S3Enabled() bool { return s.opt.S3.enabled() }

func (s *Service) s3Client() (*minio.Client, error) {
	o := s.opt.S3
	return minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
	})
}

// pruneS3 keeps only the newest keep objects in the bucket (0 = keep all).
func (s *Service) pruneS3(ctx context.Context, keep int) {
	if keep <= 0 {
		return
	}
	files, err := s.S3List(ctx)
	if err != nil || len(files) <= keep {
		return
	}
	cl, err := s.s3Client()
	if err != nil {
		return
	}
	for _, f := range files[keep:] { // S3List is newest-first
		_ = cl.RemoveObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+f.Name, minio.RemoveObjectOptions{})
	}
}

// uploadS3 puts one encrypted dump into the configured S3-compatible bucket, for
// the 3-2-1 rule (a copy that does not sit next to the DB it protects).
func (s *Service) uploadS3(ctx context.Context, name string, data []byte) error {
	cl, err := s.s3Client()
	if err != nil {
		return err
	}
	_, err = cl.PutObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+name, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

// S3Test checks that the configured bucket is reachable — the panel's
// "Verbindung testen".
func (s *Service) S3Test(ctx context.Context) error {
	if !s.S3Enabled() {
		return fmt.Errorf("S3 ist nicht konfiguriert")
	}
	cl, err := s.s3Client()
	if err != nil {
		return err
	}
	ok, err := cl.BucketExists(ctx, s.opt.S3.Bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket %q nicht gefunden", s.opt.S3.Bucket)
	}
	return nil
}

// S3List returns the encrypted dumps stored in the bucket (the bucket explorer).
func (s *Service) S3List(ctx context.Context) ([]BackupFile, error) {
	if !s.S3Enabled() {
		return nil, fmt.Errorf("S3 ist nicht konfiguriert")
	}
	cl, err := s.s3Client()
	if err != nil {
		return nil, err
	}
	var out []BackupFile
	for obj := range cl.ListObjects(ctx, s.opt.S3.Bucket,
		minio.ListObjectsOptions{Prefix: s.opt.S3.Prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		name := strings.TrimPrefix(obj.Key, s.opt.S3.Prefix)
		if !strings.HasSuffix(name, ".dump.enc") {
			continue
		}
		out = append(out, BackupFile{Name: name, Size: obj.Size, ModTime: obj.LastModified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// S3Get downloads one object from the bucket (still encrypted).
func (s *Service) S3Get(ctx context.Context, name string) ([]byte, error) {
	if !s.S3Enabled() {
		return nil, fmt.Errorf("S3 ist nicht konfiguriert")
	}
	if !validName(name) {
		return nil, fmt.Errorf("invalid backup name")
	}
	cl, err := s.s3Client()
	if err != nil {
		return nil, err
	}
	obj, err := cl.GetObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+name, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *Service) writeStatus(st Status) {
	if s.opt.StatusFile == "" {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = writeFileAtomic(s.opt.StatusFile, b)
}

// writeFileAtomic writes via a per-call unique temp file + rename so readers
// never see a partial file and two concurrent writers can't interleave into a
// shared temp (which previously corrupted status.json). os.CreateTemp yields
// 0600 perms.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeTemp writes raw dump bytes to a temp file for pg_restore (which needs a
// seekable archive) and returns a cleanup func.
func writeTemp(raw []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "treckrr-restore-*.dump")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	// Surface a failed Close: it can mean the archive was not fully flushed,
	// which would make pg_restore read a truncated file.
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}
