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

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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
	S3OK          *bool     `json:"s3_ok,omitempty"`
	Encrypted     bool      `json:"encrypted"`
	SchemaVersion string    `json:"schema_version,omitempty"`
	RestoreTested time.Time `json:"restore_tested,omitempty"`
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
	Interval    time.Duration
	S3          S3Options
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
	// Off-box copy to S3 (3-2-1). A failure is recorded but doesn't fail the
	// backup — the local encrypted dump already exists.
	if s.S3Enabled() {
		ok := s.uploadS3(ctx, name, enc) == nil
		st.S3OK = &ok
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

// ValidateArchive writes the raw (decrypted) dump to a temp file and lists its
// table of contents via pg_restore --list — proving it is a well-formed archive
// — and returns the object count. A corrupt/truncated dump fails here.
func (s *Service) ValidateArchive(ctx context.Context, raw []byte) (int, error) {
	tmp, cleanup, err := writeTemp(raw)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", tmp)
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
	cmd := exec.CommandContext(ctx, "pg_restore",
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
	enc, err := os.ReadFile(encFile) //nosec G304 -- operator-supplied backup path (CLI)
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
	enc, err := os.ReadFile(encFile) //nosec G304 -- operator-supplied backup path (CLI)
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

// S3Enabled reports whether an off-box S3 destination is configured.
func (s *Service) S3Enabled() bool { return s.opt.S3.enabled() }

func (s *Service) s3Client() (*minio.Client, error) {
	o := s.opt.S3
	return minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
	})
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
		return fmt.Errorf("Bucket %q nicht gefunden", s.opt.S3.Bucket)
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
