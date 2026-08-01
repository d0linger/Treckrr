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
	"strings"
	"time"
)

// magic prefixes every encrypted dump so a wrong/plain file is rejected early.
const magic = "TRKBK1"

// Status is the on-disk shape written after each scheduled backup and read by the
// admin panel. Timestamps are zero when the event never happened.
type Status struct {
	LastBackup    time.Time `json:"last_backup"`
	OK            bool      `json:"ok"`
	SizeBytes     int64     `json:"size_bytes"`
	OffhostOK     *bool     `json:"offhost_ok,omitempty"`
	Encrypted     bool      `json:"encrypted"`
	SchemaVersion string    `json:"schema_version,omitempty"`
	RestoreTested time.Time `json:"restore_tested,omitempty"`
}

// Options configures a Service. EncKey empty means backups are disabled.
type Options struct {
	DatabaseURL string
	EncKey      string
	Dir         string
	StatusFile  string
	Keep        int
	Interval    time.Duration
}

// Service runs and restores encrypted backups.
type Service struct {
	opt Options
	db  *sql.DB
	key []byte // 32-byte AES-256 key, nil when disabled
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

// Decrypt exposes decrypt for callers holding raw bytes (e.g. the restore CLI).
func (s *Service) Decrypt(enc []byte) ([]byte, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	return decrypt(enc, s.key)
}

// dump runs pg_dump in PostgreSQL's custom format (compressed, restorable with
// pg_restore) against the configured database and returns the raw archive bytes.
func (s *Service) dump(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg_dump",
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

// RunScheduled writes one encrypted dump to the backup dir, prunes old ones,
// verifies the fresh dump is restorable, and updates status.json.
func (s *Service) RunScheduled(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	enc, name, err := s.CreateEncrypted(ctx)
	if err != nil {
		s.writeStatus(Status{OK: false, Encrypted: true, SchemaVersion: s.SchemaVersion(ctx)})
		return err
	}
	if err := os.MkdirAll(s.opt.Dir, 0o750); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.opt.Dir, name), enc); err != nil {
		return err
	}
	s.prune()
	st := Status{
		LastBackup:    time.Now(),
		OK:            true,
		SizeBytes:     int64(len(enc)),
		Encrypted:     true,
		SchemaVersion: s.SchemaVersion(ctx),
	}
	// A backup you have never restored is not a backup: confirm the fresh dump
	// decrypts and is a well-formed archive before recording success.
	if err := s.verifyRestorable(ctx, enc); err == nil {
		st.RestoreTested = time.Now()
	}
	s.writeStatus(st)
	return nil
}

// prune keeps only the newest opt.Keep dumps in the backup dir.
func (s *Service) prune() {
	if s.opt.Keep <= 0 {
		return
	}
	entries, err := filepath.Glob(filepath.Join(s.opt.Dir, "treckrr-*.dump.enc"))
	if err != nil {
		return
	}
	if len(entries) <= s.opt.Keep {
		return
	}
	sort.Strings(entries) // timestamped names sort chronologically
	for _, old := range entries[:len(entries)-s.opt.Keep] {
		_ = os.Remove(old)
	}
}

// verifyRestorable decrypts the archive and lists its table of contents via
// pg_restore --list — a cheap integrity check that a corrupt/wrong-key/truncated
// file fails.
func (s *Service) verifyRestorable(ctx context.Context, enc []byte) error {
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return err
	}
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", tmp)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore --list: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// Restore decrypts a backup file and restores it into targetURL, dropping and
// recreating objects (--clean). This overwrites data — callers must confirm.
func (s *Service) Restore(ctx context.Context, encFile, targetURL string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	enc, err := os.ReadFile(encFile) //nosec G304 -- operator-supplied backup path (CLI)
	if err != nil {
		return err
	}
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return err
	}
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "pg_restore",
		"--clean", "--if-exists", "--no-owner", "--dbname="+targetURL, tmp)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// TestReport summarizes a test-restore.
type TestReport struct {
	Objects       int
	SchemaVersion string
}

// TestRestore validates a backup without touching the live DB: it decrypts and
// lists the archive. It is the integrity drill behind the panel's "Restore
// getestet" marker.
func (s *Service) TestRestore(ctx context.Context, encFile string) (TestReport, error) {
	var rep TestReport
	if !s.Enabled() {
		return rep, ErrDisabled
	}
	enc, err := os.ReadFile(encFile) //nosec G304 -- operator-supplied backup path (CLI)
	if err != nil {
		return rep, err
	}
	raw, err := decrypt(enc, s.key)
	if err != nil {
		return rep, err
	}
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return rep, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", tmp)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return rep, fmt.Errorf("pg_restore --list: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, ";") {
			rep.Objects++
		}
	}
	rep.SchemaVersion = s.SchemaVersion(ctx)
	return rep, nil
}

// Loop runs a scheduled backup shortly after boot, then on opt.Interval, until
// ctx is canceled. No-op when disabled.
func (s *Service) Loop(ctx context.Context, logf func(string, ...any)) {
	if !s.Enabled() {
		return
	}
	run := func() {
		c, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if err := s.RunScheduled(c); err != nil {
			logf("scheduled backup failed: %v", err)
		} else {
			logf("scheduled backup written to %s", s.opt.Dir)
		}
	}
	run()
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
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

// writeFileAtomic writes via a temp file + rename so readers never see a partial file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
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
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}
