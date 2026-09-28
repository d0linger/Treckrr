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
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/argon2"

	"github.com/d0linger/treckrr/internal/metrics"
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
// Unknown-but-valid expressions fall back to showing the raw expression. A
// schedule that fires in the 02:00–02:59 DST window carries a warning.
func DescribeCron(expr string) string {
	desc := describeCron(expr)
	if warning := CronDSTWarning(expr); warning != "" {
		desc += " " + warning
	}
	return desc
}

// CronDSTWarning explains the daylight-saving hazard of a schedule whose hour
// field selects 02:xx but not every hour. The scheduler runs in local time
// (TZ=Europe/Vienna): on the spring change 02:00–02:59 does not exist, so the
// run slips by a whole day; on the autumn change that hour happens twice.
// Empty means no warning (including off, invalid, or hourly schedules).
func CronDSTWarning(expr string) string {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return ""
	}
	if _, ok := parseCron(expr); !ok {
		return ""
	}
	hours, ok := cronFieldValues(fields[1], 0, 23)
	if !ok || len(hours) == 24 || !hours[2] {
		return ""
	}
	return "Achtung: Zwischen 02:00 und 02:59 Uhr fällt der Lauf bei der Umstellung auf Sommerzeit aus und läuft bei der Umstellung auf Winterzeit doppelt — besser eine Zeit ab 03:00 Uhr wählen."
}

// cronFieldValues expands one numeric cron field (lists, ranges and steps) to
// the set of values it selects. Names or other syntax report ok=false.
func cronFieldValues(field string, lo, hi int) (map[int]bool, bool) {
	values := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n <= 0 {
				return nil, false
			}
			step = n
		}
		start, end := lo, hi
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			x, errA := strconv.Atoi(a)
			y, errB := strconv.Atoi(b)
			if errA != nil || errB != nil {
				return nil, false
			}
			start, end = x, y
		default:
			x, err := strconv.Atoi(rangePart)
			if err != nil {
				return nil, false
			}
			start, end = x, x
			if hasStep {
				end = hi
			}
		}
		if start < lo || end > hi || start > end {
			return nil, false
		}
		for v := start; v <= end; v += step {
			values[v] = true
		}
	}
	return values, true
}

// describeCron is the plain rendering behind DescribeCron.
func describeCron(expr string) string {
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

// A magic header prefixes every encrypted dump so a wrong/plain file is rejected
// early. TRKBK1 derived the AES key as a bare sha256(secret); TRKBK2 uses
// Argon2id over a per-file random salt (memory-hard, salted) and stores that
// salt after the magic. Both are still decryptable; new dumps are always TRKBK2.
const (
	magicV1  = "TRKBK1"
	magicV2  = "TRKBK2"
	magicLen = 6 // both magics are 6 bytes
	saltLen  = 16
	// Argon2id parameters (moderate): 64 MiB, 1 pass, 4 lanes → 32-byte AES key.
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
)

// deriveKey turns the raw backup secret into a 32-byte AES-256 key using Argon2id
// over the given salt.
func deriveKey(secret, salt []byte) []byte {
	return argon2.IDKey(secret, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

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
	// ArchiveVerified is the cheap post-write drill (decrypt + pg_restore --list
	// + TOC plausibility) that runs on every backup. RestoreTested is the real
	// thing: a load into a scratch database. Keeping them apart stops the panel
	// from promising a rehearsal that never happened.
	ArchiveVerified time.Time `json:"archive_verified,omitempty"`
	RehearsalNote   string    `json:"rehearsal_note,omitempty"`
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
	Endpoint     string
	Bucket       string
	AccessKey    string
	SecretKey    string
	Prefix       string
	LegacyPrefix *string
	UseSSL       bool
}

// enabled reports whether every non-secret S3 namespace field is configured.
func (o S3Options) enabled() bool {
	return o.Endpoint != "" && o.Bucket != "" && strings.TrimSpace(o.Prefix) != ""
}

// Options configures a Service. EncKey empty means backups are disabled.
type Options struct {
	DatabaseURL string
	EncKey      string
	Dir         string
	StatusFile  string
	Keep        int
	// SkipStartupCleanup is for short-lived CLI services. They must not remove
	// another process's staging files before offline restore admission succeeds.
	SkipStartupCleanup bool
	// MaxBytes bounds each in-memory archive. Zero uses the online 128 MiB
	// budget; a larger value is for an explicitly provisioned offline CLI only.
	MaxBytes int64
	// RehearseURL points at a server where a scratch database may be created and
	// dropped for restore rehearsals. Empty disables rehearsals entirely — the
	// conservative default, because the feature needs CREATE DATABASE rights.
	RehearseURL string
	S3          S3Options
	// SettingsFn returns the current GUI-editable schedule. When nil the service
	// falls back to a fixed default cron and Keep. Called each scheduler tick so
	// GUI edits apply without a restart.
	SettingsFn func(context.Context) Settings
	// FirstTickDelay postpones the scheduler's first tick after boot, so a job
	// that killed the previous process is not restarted within seconds of the
	// container coming back. Zero uses a jittered default (see bootDelay);
	// a negative value disables the delay.
	FirstTickDelay time.Duration
}

// BackupFile describes one stored encrypted dump for the admin panel list.
type BackupFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Service runs and restores encrypted backups.
type Service struct {
	opt    Options
	db     *sql.DB
	secret []byte // raw backup-key secret; the AES key is derived per file, nil when disabled

	mu         sync.Mutex    // serializes status.json read-modify-write
	opSem      chan struct{} // size-1 semaphore serializing DB dump/restore, ctx-aware (T-05)
	workSem    chan struct{} // one whole memory-heavy job, including HTTP parsing
	volRetryAt time.Time     // earliest next volume attempt after a failure (tick goroutine only)
	s3RetryAt  time.Time     // earliest next S3 attempt after a failure (tick goroutine only)

	s3mu     sync.Mutex    // guards the cached S3 client
	s3cl     *minio.Client // one client (and HTTP transport) reused across S3 calls
	s3clOpts s3ClientOptions
}

// s3ClientOptions is the comparable subset of S3 settings used to build a
// client. Keeping the values directly avoids misclassifying a cache identity as
// password hashing while still rebuilding when credentials rotate.
type s3ClientOptions struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// statusRetryBackoff is how long the scheduler waits before retrying a failed
// destination, so a persistent failure (DB down, disk full) doesn't turn into a
// heavy pg_dump loop every minute.
const statusRetryBackoff = 15 * time.Minute

// updateStatus applies mutate to status.json under the lock, re-reading first so
// a concurrent run's fields (e.g. the other destination's timestamps) are
// preserved instead of clobbered by a stale read-modify-write.
func (s *Service) updateStatus(mutate func(*Status)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.readStatus()
	mutate(&st)
	return s.writeStatus(st)
}

// withFailedStatus records a failed backup without hiding either the original
// operation error or a status-persistence failure.
func (s *Service) withFailedStatus(cause error) error {
	statusErr := s.updateStatus(func(st *Status) { st.OK = false })
	if statusErr != nil {
		statusErr = fmt.Errorf("persist failed backup status: %w", statusErr)
	}
	return errors.Join(cause, statusErr)
}

// New builds a Service. When opt.EncKey is empty the service is disabled and all
// operations return ErrDisabled.
func New(opt Options, db *sql.DB) *Service {
	s := &Service{opt: opt, db: db, opSem: make(chan struct{}, 1), workSem: make(chan struct{}, 1)}
	if opt.EncKey != "" {
		s.secret = []byte(opt.EncKey)
	}
	if !opt.SkipStartupCleanup {
		s.cleanupLeftovers()
	}
	return s
}

// cleanupLeftovers deals with what a crash or a human left in the backup dir.
// A *.staging file is by definition an incomplete write — the atomic rename
// never happened — so one older than an hour is garbage and is removed (and
// logged; prune's glob never matches it, so it would otherwise sit forever).
// Plain *.dump files are NOT ours to delete — an operator may have created them
// deliberately — but they escape rotation entirely, so their presence is at
// least flagged.
func (s *Service) cleanupLeftovers() {
	if s.opt.Dir == "" {
		return
	}
	stale, _ := filepath.Glob(filepath.Join(s.opt.Dir, "*.dump.enc.staging"))
	interrupted, _ := filepath.Glob(filepath.Join(s.opt.Dir, "*.dump.enc.staging.*.tmp"))
	stale = append(stale, interrupted...)
	for _, f := range stale {
		fi, err := os.Lstat(f)
		if err != nil {
			continue
		}
		if !fi.Mode().IsRegular() || time.Since(fi.ModTime()) <= time.Hour {
			continue
		}
		if err := os.Remove(f); err != nil {
			slog.Warn("backup: stale staging file not removable", "file", filepath.Base(f), "err", err)
		} else {
			slog.Info("backup: removed stale staging file", "file", filepath.Base(f))
		}
	}
	if plain, _ := filepath.Glob(filepath.Join(s.opt.Dir, "*.dump")); len(plain) > 0 {
		slog.Warn("backup dir contains unencrypted .dump files outside rotation", "count", len(plain))
	}
	s.removeStalePlainTemps(plainTempMaxAge)
}

// plainTempPattern names the private plaintext scratch files (pg_dump output,
// decrypted archives for pg_restore) kept in the backup directory instead of a
// RAM-backed /tmp. They never match the "treckrr-*.dump.enc" rotation glob.
const plainTempPattern = ".treckrr-plain-*.tmp"

// plainTempMaxAge is how old a plaintext scratch file must be before cleanup
// treats it as a crash leftover. It exceeds every bounded operation that uses
// one (backup runs and restores: 10 min; rehearsals: rehearsalTimeout), so a
// live operation in another process never loses its file.
const plainTempMaxAge = 30 * time.Minute

// removeStalePlainTemps deletes plaintext scratch files a crashed process left
// behind. They hold an unencrypted copy of the database, so they must not
// accumulate on the persistent backup volume.
func (s *Service) removeStalePlainTemps(maxAge time.Duration) {
	if s.opt.Dir == "" {
		return
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.opt.Dir, plainTempPattern))
	for _, f := range leftovers {
		fi, err := os.Lstat(f)
		if err != nil || !fi.Mode().IsRegular() || time.Since(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(f); err != nil {
			slog.Warn("backup: stale plaintext scratch file not removable", "file", filepath.Base(f), "err", err)
		} else {
			slog.Info("backup: removed stale plaintext scratch file", "file", filepath.Base(f))
		}
	}
}

// acquireOp serializes DB dump/restore operations, honoring ctx cancellation and
// deadlines (T-05). Pair a nil-error return with a deferred releaseOp.
func (s *Service) acquireOp(ctx context.Context) error {
	select {
	case s.opSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseOp() { <-s.opSem }

// Enabled reports whether a backup key is configured.
func (s *Service) Enabled() bool { return s.secret != nil }

// ErrDisabled is returned when no BACKUP_ENCRYPTION_KEY is configured.
var ErrDisabled = fmt.Errorf("backups are not configured (set BACKUP_ENCRYPTION_KEY)")

// filenameLayout is the timestamp embedded in backup names.
const filenameLayout = "2006-01-02-150405.000000000"

// Filename is the collision-resistant name of a dump taken at t. Nanoseconds
// prevent two serialized manual/scheduled runs in the same second from
// replacing one another locally or in S3. The stamp is UTC with a "Z" suffix,
// so names never repeat or run backwards across a daylight-saving change.
// Older releases wrote local time without a suffix; archiveTime reads both.
func Filename(t time.Time) string {
	return "treckrr-" + t.UTC().Format(filenameLayout) + "Z.dump.enc"
}

// archiveTime returns the creation time encoded in a backup name. UTC ("Z")
// names are exact; legacy names carry local wall-clock time and are read in
// time.Local, the zone that wrote them.
func archiveTime(name string) (time.Time, bool) {
	return archiveTimeIn(name, time.Local)
}

// archiveTimeIn is archiveTime with an explicit zone for legacy names.
func archiveTimeIn(name string, legacy *time.Location) (time.Time, bool) {
	stamp, ok := strings.CutPrefix(name, "treckrr-")
	if !ok {
		return time.Time{}, false
	}
	if stamp, ok = strings.CutSuffix(stamp, ".dump.enc"); !ok {
		return time.Time{}, false
	}
	if utc, isUTC := strings.CutSuffix(stamp, "Z"); isUTC {
		t, err := time.ParseInLocation(filenameLayout, utc, time.UTC)
		return t, err == nil
	}
	for _, layout := range []string{filenameLayout, "2006-01-02-150405"} {
		if t, err := time.ParseInLocation(layout, stamp, legacy); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// sortNewestFirst orders backups by the real time encoded in their names, so
// UTC and legacy local-time names interleave correctly. Names without a
// parseable stamp fall back to their modification time.
func sortNewestFirst(files []BackupFile) {
	at := func(f BackupFile) time.Time {
		if t, ok := archiveTime(f.Name); ok {
			return t
		}
		return f.ModTime
	}
	sort.SliceStable(files, func(i, j int) bool {
		ti, tj := at(files[i]), at(files[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return files[i].Name > files[j].Name
	})
}

// encrypt seals plaintext with AES-256-GCM into the TRKBK2 layout:
// magicV2 || salt(16) || nonce || ciphertext(+tag). The AES key is derived from
// the raw secret via Argon2id over the per-file random salt. One-shot (the dump
// fits comfortably in memory); the GCM tag covers the whole message, so any
// truncation or tampering fails on decrypt.
func encrypt(plaintext, secret []byte) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(deriveKey(secret, salt))
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
	out := append([]byte(magicV2), salt...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt for both formats, choosing the key derivation from
// the magic header: TRKBK2 → Argon2id over the stored salt; TRKBK1 (legacy) →
// bare sha256(secret). A wrong/plain file is rejected on the header.
func decrypt(enc, secret []byte) ([]byte, error) {
	var key []byte
	switch {
	case len(enc) >= magicLen+saltLen && string(enc[:magicLen]) == magicV2:
		salt := enc[magicLen : magicLen+saltLen]
		key = deriveKey(secret, salt)
		enc = enc[magicLen+saltLen:]
	case len(enc) >= magicLen && string(enc[:magicLen]) == magicV1:
		k := sha256.Sum256(secret)
		key = k[:]
		enc = enc[magicLen:]
	default:
		return nil, fmt.Errorf("not a Treckrr backup (bad header)")
	}
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

// createScratch opens a new private (0600) plaintext scratch file. It lives in
// the backup directory: the default /tmp is a RAM-backed tmpfs charged to the
// container's memory limit, so a disk-backed file keeps a dump from counting
// twice against it. Without a configured directory the OS temp dir is used.
func (s *Service) createScratch() (*os.File, error) {
	dir := os.TempDir()
	if s.opt.Dir != "" {
		if err := os.MkdirAll(s.opt.Dir, 0o750); err != nil {
			return nil, err
		}
		dir = s.opt.Dir
	}
	return os.CreateTemp(dir, plainTempPattern)
}

// dumpToFile runs pg_dump in PostgreSQL's custom format (compressed, restorable
// with pg_restore) and streams the archive into a private scratch file. It
// returns the file's path and size plus a cleanup func the caller must run.
// Streaming replaces an in-memory buffer that grew by doubling.
func (s *Service) dumpToFile(ctx context.Context) (string, int64, func(), error) {
	// Serialize against any restore (and other dumps): a dump taken while a restore
	// is mid-flight would capture an inconsistent, partially-restored schema (T-05).
	if err := s.acquireOp(ctx); err != nil {
		return "", 0, nil, err
	}
	defer s.releaseOp()
	// DATABASE_URL is trusted deployment config, not user input; the password is
	// passed via PGPASSWORD (see dbURLEnv), not on the command line.
	dbURL, env, err := dbURLEnv(s.opt.DatabaseURL)
	if err != nil {
		return "", 0, nil, err
	}
	f, err := s.createScratch()
	if err != nil {
		return "", 0, nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	cmd := exec.CommandContext(ctx, "pg_dump", // #nosec G204
		"--format=custom", "--no-owner", "--no-privileges", dbURL)
	cmd.Env = env
	var errBuf bytes.Buffer
	// Include space for the TRKBK2 salt, nonce and tag in the archive budget.
	cmd.Stdout = &limitedWriter{w: f, remaining: s.maxBytes() - 50}
	cmd.Stderr = &limitedWriter{w: &errBuf, remaining: 1 << 20}
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		cleanup()
		return "", 0, nil, fmt.Errorf("pg_dump: %w: %s", runErr, strings.TrimSpace(errBuf.String()))
	}
	if closeErr != nil {
		cleanup()
		return "", 0, nil, closeErr
	}
	fi, err := os.Stat(path)
	if err != nil {
		cleanup()
		return "", 0, nil, err
	}
	return path, fi.Size(), cleanup, nil
}

// encryptFile seals the plaintext archive at path (exactly size bytes) into the
// TRKBK2 layout of encrypt. The file is read directly behind the header and
// sealed in place, so plaintext and ciphertext never exist as two full copies
// in memory. It also returns the plaintext's SHA-256 for verifyEncrypted.
func encryptFile(path string, size int64, secret []byte) ([]byte, [32]byte, error) {
	var sum [32]byte
	if size < 0 || size > 16<<30 {
		return nil, sum, fmt.Errorf("archive size out of bounds: %d", size)
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, sum, err
	}
	block, err := aes.NewCipher(deriveKey(secret, salt))
	if err != nil {
		return nil, sum, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, sum, err
	}
	header := magicLen + saltLen + gcm.NonceSize()
	buf := make([]byte, header+int(size), header+int(size)+gcm.Overhead())
	copy(buf, magicV2)
	copy(buf[magicLen:], salt)
	nonce := buf[magicLen+saltLen : header]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, sum, err
	}
	f, err := os.Open(path) // #nosec G304 -- service-owned scratch file
	if err != nil {
		return nil, sum, err
	}
	defer f.Close()
	plain := buf[header:]
	if _, err := io.ReadFull(f, plain); err != nil {
		return nil, sum, fmt.Errorf("read archive: %w", err)
	}
	// The archive must not have grown after its size was taken.
	if n, _ := f.Read(make([]byte, 1)); n != 0 {
		return nil, sum, errors.New("archive changed while it was being encrypted")
	}
	sum = sha256.Sum256(plain)
	// plain[:0] as dst is GCM's documented in-place mode; buf has room for the tag.
	sealed := gcm.Seal(plain[:0], nonce, plain, nil)
	return buf[:header+len(sealed)], sum, nil
}

// verifyEncrypted proves enc decrypts with the service key back to exactly the
// plaintext whose SHA-256 is want: the bytes about to be stored are readable.
func (s *Service) verifyEncrypted(enc []byte, want [32]byte) error {
	raw, err := decrypt(enc, s.secret)
	if err != nil {
		return err
	}
	if sha256.Sum256(raw) != want {
		return errors.New("encrypted archive does not decrypt to the dumped bytes")
	}
	return nil
}

// createEncrypted dumps the database and encrypts it. With verify it also
// proves the plaintext is a plausible, well-formed Treckrr archive and that
// the ciphertext decrypts back to it — the drill every stored copy must pass.
// Peak memory is about two archive sizes (ciphertext plus the verification
// decrypt), instead of several buffered and temp-file copies.
func (s *Service) createEncrypted(ctx context.Context, verify bool) ([]byte, string, error) {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	if !s.Enabled() {
		return nil, "", ErrDisabled
	}
	path, size, cleanup, err := s.dumpToFile(ctx)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	if verify {
		if _, err := s.validateArchiveFile(ctx, path); err != nil {
			return nil, "", err
		}
	}
	enc, sum, err := encryptFile(path, size, s.secret)
	if err != nil {
		return nil, "", err
	}
	cleanup() // the plaintext is no longer needed; do not keep it on disk longer
	if verify {
		if err := s.verifyEncrypted(enc, sum); err != nil {
			return nil, "", err
		}
	}
	return enc, Filename(time.Now()), nil
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
// on-demand download. Stored copies use createVerifiedDump instead.
func (s *Service) CreateEncrypted(ctx context.Context) (data []byte, filename string, err error) {
	return s.createEncrypted(ctx, false)
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
	b, err := os.ReadFile(s.opt.StatusFile) // #nosec G304 G703 -- operator-configured status file path
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
	now := time.Now()
	volumeErr := s.runVolume(ctx, set.VolumeKeep)
	if stateErr := s.persistSchedulerResult(ctx, "volume", volumeErr == nil, now); volumeErr != nil || stateErr != nil {
		return errors.Join(volumeErr, stateErr)
	}
	if s.S3Enabled() {
		// Surface the S3 result: the volume dump already succeeded above, but a
		// failed off-box copy must not be reported as overall success (3-2-1), so
		// the CLI `treckrr backup` exits non-zero and cron can alert.
		s3Err := s.runS3Mirror(ctx, set.S3Keep)
		return errors.Join(s3Err, s.persistSchedulerResult(ctx, "s3", s3Err == nil, time.Now()))
	}
	return nil
}

// runVolume writes one encrypted dump to the volume, prunes to keep, verifies it
// restores, and updates status — without touching S3.
func (s *Service) runVolume(ctx context.Context, keep int) error {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Verify BEFORE the dump becomes visible and before pruning — a backup you have
	// never restored is not a backup. createVerifiedDump validates the plaintext
	// archive (pg_restore --list + TOC plausibility) and proves the ciphertext
	// decrypts back to it. On failure keep every prior backup (do NOT prune),
	// record OK=false and return the error (T-04, fail closed): a corrupt new dump
	// must never replace the last good recovery point.
	enc, name, err := s.createVerifiedDump(ctx)
	if err != nil {
		return s.withFailedStatus(fmt.Errorf("backup verification failed, prior backups kept: %w", err))
	}
	if err := os.MkdirAll(s.opt.Dir, 0o750); err != nil {
		return s.withFailedStatus(err)
	}
	// Write to a staging name first. List() only returns *.dump.enc, so the staging
	// file (…​.dump.enc.staging) is never listed or S3-mirrored until it is
	// completely written and renamed into place.
	finalPath := filepath.Join(s.opt.Dir, name)
	stagingPath := finalPath + ".staging"
	if err := writeFileAtomic(stagingPath, enc); err != nil {
		return s.withFailedStatus(err)
	}
	// Verified and durable — atomically promote it to the listable final name.
	if err := durableRename(stagingPath, finalPath); err != nil {
		_ = os.Remove(stagingPath)
		return s.withFailedStatus(err)
	}
	// Now it is safe to prune older backups to `keep`.
	s.prune(keep)
	schema := s.SchemaVersion(ctx)
	now := time.Now()
	return s.updateStatus(func(st *Status) {
		st.Encrypted = true
		st.SchemaVersion = schema
		st.LastBackup = now
		st.OK = true
		st.SizeBytes = int64(len(enc))
		// RestoreTested is deliberately NOT set here. This path ran
		// verifyRestorable, which reads the archive's table of contents and checks
		// the object names — it never asks pg_restore to load anything. Claiming a
		// tested restore for that is the difference between "the file parses" and
		// "we can come back from this". Only RehearseRestore, which restores into a
		// scratch database and queries it, stamps that field.
		st.ArchiveVerified = now
	})
}

// runS3Mirror uploads the newest volume dump not already in the bucket, prunes S3
// to s3keep, and records the S3 status/time.
func (s *Service) runS3Mirror(ctx context.Context, s3keep int) error {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return err
	}
	defer release()
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
		// A failed listing must fail the run. Treating it as an empty bucket
		// re-uploads an object that already exists (a conditional PUT then
		// fails with 412) and silently skips retention.
		remote, listErr := s.S3List(ctx)
		if listErr != nil {
			return s.recordS3Result(fmt.Errorf("s3 list: %w", listErr))
		}
		for _, r := range remote {
			if r.Name == newest { // already mirrored — verify the stored copy before trusting it
				// A prior run may have uploaded this object but failed verification and
				// left it un-pruned; blindly reporting S3OK here would mask a corrupt
				// off-box copy forever. Re-verify the stored bytes (also catches at-rest
				// bit-rot); if it's bad but we still hold a good local dump, self-heal by
				// overwriting it rather than staying failed until the next volume cycle.
				owned, ownErr := s.s3ObjectOwned(ctx, newest)
				if ownErr != nil {
					return ownErr
				}
				if !owned {
					// The name is occupied by a legacy or foreign object. Never send it
					// through repair: create a distinct, owned recovery point instead.
					if data, name, err = s.createVerifiedDump(ctx); err != nil {
						return err
					}
					break
				}
				err := s.verifyMirroredCopy(ctx, newest)
				if err == nil {
					s.pruneS3(ctx, s3keep)
				}
				return s.recordS3Result(err)
			}
		}
		if name == "" {
			name = newest
			if data, err = s.OpenContext(ctx, newest); err != nil {
				return err
			}
			// The stored copy is compared bit for bit with these bytes, so they
			// must pass the full drill before they are uploaded.
			if err := s.verifyRestorable(ctx, data); err != nil {
				return s.recordS3Result(fmt.Errorf("local dump %s not restorable: %w", newest, err))
			}
		}
	} else {
		// No local volume dump (e.g. the volume schedule is off) — create a fresh
		// one just for S3 so the off-box copy still happens rather than silently
		// never running. Verify it restores before it becomes the ONLY off-box copy —
		// an unverified
		// backup is not a backup. runVolume verifies before promoting a local dump;
		// this fresh S3-only dump gets the same drill. On failure, don't upload.
		if data, name, err = s.createVerifiedDump(ctx); err != nil {
			return err
		}
	}
	// A PUT returning nil is not proof the bytes landed complete and readable
	// off-box — uploadVerifiedS3 re-reads the stored object before trusting it.
	uerr := s.uploadVerifiedS3(ctx, name, data)
	if uerr == nil {
		s.pruneS3(ctx, s3keep)
	}
	return s.recordS3Result(uerr)
}

// recordS3Result stores the outcome of one S3 mirror attempt in status.json
// and returns the attempt's error joined with any status-persistence error.
func (s *Service) recordS3Result(runErr error) error {
	ok := runErr == nil
	now := time.Now()
	statusErr := s.updateStatus(func(st *Status) { st.S3OK, st.LastS3 = &ok, now })
	return errors.Join(runErr, statusErr)
}

// verifyMirroredCopy re-checks an object this installation already uploaded:
// the local archive must pass the full drill, and the stored bytes must be
// identical to it. A mismatch (corrupt or truncated remote) is repaired by
// replacing the owned object with the verified local archive.
func (s *Service) verifyMirroredCopy(ctx context.Context, name string) error {
	data, err := s.OpenContext(ctx, name)
	if err != nil {
		return err
	}
	if err := s.verifyRestorable(ctx, data); err != nil {
		return fmt.Errorf("local dump %s not restorable: %w", name, err)
	}
	verr := s.verifyS3Object(ctx, name, int64(len(data)), sha256.Sum256(data))
	if verr == nil {
		return nil
	}
	if rerr := s.repairS3Mirror(ctx, name, data); rerr != nil {
		return fmt.Errorf("s3 copy failed verification (%v) and repair failed: %w", verr, rerr)
	}
	return nil // repaired: the overwritten object re-verified
}

// createVerifiedDump creates a fresh encrypted recovery point whose archive and
// encryption have both been verified, before it can become a stored copy.
func (s *Service) createVerifiedDump(ctx context.Context) ([]byte, string, error) {
	return s.createEncrypted(ctx, true)
}

// repairS3Mirror replaces a corrupt object only after ownership metadata proves
// this installation created it, then re-verifies the replacement. data is the
// local archive, already verified by the caller, so a bad local dump is never
// pushed over the (differently) bad remote — repair only ever replaces a bad
// off-box copy with a known-good one.
func (s *Service) repairS3Mirror(ctx context.Context, name string, data []byte) error {
	owned, err := s.s3ObjectOwned(ctx, name)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("refusing to replace unowned S3 object %q", name)
	}
	cl, err := s.s3Client()
	if err != nil {
		return err
	}
	if err := cl.RemoveObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+name, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove corrupt owned S3 object: %w", err)
	}
	if err := s.uploadS3(ctx, name, data); err != nil {
		return err
	}
	return s.verifyS3Object(ctx, name, int64(len(data)), sha256.Sum256(data))
}

// minRetention is the age below which a dump is never pruned, regardless of
// count. This protects recent points from immediate deletion; it does not
// guarantee daily/weekly spacing or preserve older points after many retries.
const minRetention = 24 * time.Hour

// prune keeps the newest keep dumps in the volume backup dir, but never deletes
// one younger than minRetention. "Newest" is the real time encoded in each
// name, so UTC names and legacy local-time names are ranked correctly.
func (s *Service) prune(keep int) {
	if keep <= 0 {
		return
	}
	files, err := s.List()
	if err != nil || len(files) <= keep {
		return
	}
	cutoff := time.Now().Add(-minRetention)
	for _, old := range files[keep:] {
		if old.ModTime.After(cutoff) {
			continue // too young to evict, even though the count says otherwise
		}
		if err := os.Remove(filepath.Join(s.opt.Dir, old.Name)); err != nil {
			// A full disk or permission slip must not stay invisible until the
			// volume runs over.
			slog.Warn("backup prune: remove failed", "file", old.Name, "err", err)
		}
	}
}

// listArchive lists the archive's table of contents via pg_restore --list — proving
// it is a well-formed archive — and returns the object count plus the raw TOC text
// (for content-sanity checks). A corrupt/truncated dump fails here.
func (s *Service) listArchive(ctx context.Context, raw []byte) (int, string, error) {
	tmp, cleanup, err := s.writeTemp(raw)
	if err != nil {
		return 0, "", err
	}
	defer cleanup()
	return listArchiveFile(ctx, tmp)
}

// listArchiveFile is listArchive for an archive that is already on disk.
func listArchiveFile(ctx context.Context, tmp string) (int, string, error) {
	// tmp is a service-owned scratch file, not user input.
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", tmp) // #nosec G204
	var out, errBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, remaining: 16 << 20}
	cmd.Stderr = &limitedWriter{w: &errBuf, remaining: 1 << 20}
	if err := cmd.Run(); err != nil {
		return 0, "", fmt.Errorf("not a valid archive: %s", strings.TrimSpace(errBuf.String()))
	}
	toc := out.String()
	n := 0
	for _, line := range strings.Split(toc, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, ";") {
			n++
		}
	}
	return n, toc, nil
}

// ValidateArchive proves the decrypted bytes are a well-formed archive AND that its
// contents look like a real Treckrr database, returning the object count. Shared by
// the backup-time drill (verifyRestorable) and the restore path (before RestoreRaw),
// so an empty/truncated/foreign archive is rejected before it can overwrite the DB.
func (s *Service) ValidateArchive(ctx context.Context, raw []byte) (int, error) {
	n, toc, err := s.listArchive(ctx, raw)
	if err != nil {
		return 0, err
	}
	if err := contentSanity(toc, n); err != nil {
		return 0, err
	}
	return n, nil
}

// validateArchiveFile is ValidateArchive for a plaintext archive on disk.
func (s *Service) validateArchiveFile(ctx context.Context, path string) (int, error) {
	n, toc, err := listArchiveFile(ctx, path)
	if err != nil {
		return 0, err
	}
	if err := contentSanity(toc, n); err != nil {
		return 0, err
	}
	return n, nil
}

// Content-sanity floor for a verified dump: a well-formed archive can still be a dump
// of the wrong or an empty database. A real Treckrr dump carries the full migrated
// schema (~188 TOC objects even with no business rows) plus these core tables, so a
// generous floor + their presence rejects a truncated/foreign/empty archive without
// risking a false reject of a legitimate backup. The required tables are all from the
// first migration, so this holds even when RESTORING a very old backup (later tables
// like invoices are deliberately NOT required, so pre-invoice dumps still restore).
const minArchiveObjects = 40

var requiredRelations = []string{"users", "neighbors", "entries"}

// contentSanity rejects a structurally-valid archive whose contents don't look like a
// real Treckrr database. Runs on the decrypted TOC as the last verify-before-promote
// step, so a corrupt new dump can never replace the last good recovery point.
func contentSanity(toc string, objects int) error {
	if objects < minArchiveObjects {
		return fmt.Errorf("archive holds only %d objects (< %d) — dump looks truncated or empty", objects, minArchiveObjects)
	}
	for _, rel := range requiredRelations {
		// pg_restore --list line: "231; 1259 16519 TABLE public neighbors treckrr".
		// The trailing space anchors the full name (so "entries" ≠ "entry_photos").
		if !strings.Contains(toc, "TABLE public "+rel+" ") {
			return fmt.Errorf("core table %q missing from archive TOC — wrong or incomplete dump", rel)
		}
	}
	return nil
}

// RestoreRaw restores decrypted dump bytes into targetURL, dropping and
// recreating objects (--clean). Destructive — callers must confirm.
func (s *Service) RestoreRaw(ctx context.Context, raw []byte, targetURL string) error {
	if int64(len(raw)) > s.maxBytes() {
		return errors.New("restore archive exceeds configured memory budget")
	}
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Serialize every dump/restore so a restore never overlaps a scheduled backup
	// or another restore (T-05).
	if err := s.acquireOp(ctx); err != nil {
		return err
	}
	defer s.releaseOp()
	// [T-05] Server-level maintenance mode (a flag + gate middleware that 503s normal
	// traffic so in-flight requests can't observe the mid-restore schema) is wired at
	// the HTTP layer: the server's handleBackupRestore sets it around this call and
	// ReconcileAfterRestore. This package stays HTTP-agnostic and only serializes the
	// op + resets the pool afterwards.
	tmp, cleanup, err := s.writeTemp(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	// targetURL is trusted deployment config; its password is passed via
	// PGPASSWORD (see dbURLEnv), not on the command line.
	dbURL, env, err := dbURLEnv(targetURL)
	if err != nil {
		return err
	}
	// Omit ephemeral authentication rows in the restore transaction itself.
	// A crash after COMMIT but before reconciliation must not resurrect sessions.
	list, err := restoreList(ctx, tmp)
	if err != nil {
		return err
	}
	listFile, removeList, err := s.writeTemp(list)
	if err != nil {
		return err
	}
	defer removeList()
	// --single-transaction wraps the whole clean+recreate+load in one transaction
	// (implies --exit-on-error): on any failure it rolls back, so a timeout or tool
	// error can no longer leave a partially restored database (T-05).
	cmd := exec.CommandContext(ctx, "pg_restore", // #nosec G204
		"--clean", "--if-exists", "--no-owner", "--single-transaction", "--use-list="+listFile, "--dbname="+dbURL, tmp)
	cmd.Env = env
	var errBuf bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &errBuf, remaining: 1 << 20}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// verifyRestorable is the automatic post-write drill: decrypt with the service key,
// then prove the archive is well-formed and its contents look like a real Treckrr DB.
func (s *Service) verifyRestorable(ctx context.Context, enc []byte) error {
	raw, err := decrypt(enc, s.secret)
	if err != nil {
		return err
	}
	_, err = s.ValidateArchive(ctx, raw)
	return err
}

// DecryptWith decrypts using an operator-supplied key string — used by the GUI
// restore, where the key is re-entered rather than taken from the environment.
func DecryptWith(enc []byte, keySecret string) ([]byte, error) {
	return decrypt(enc, []byte(keySecret))
}

// Restore decrypts a backup file (with the service key) and restores it. CLI use.
func (s *Service) Restore(ctx context.Context, encFile, targetURL string) error {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !s.Enabled() {
		return ErrDisabled
	}
	// encFile is an operator-supplied path from the restore CLI, not user input.
	enc, err := s.readFile(encFile)
	if err != nil {
		return err
	}
	raw, err := decrypt(enc, s.secret)
	if err != nil {
		return err
	}
	if _, err := s.ValidateArchive(ctx, raw); err != nil {
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
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return rep, err
	}
	defer release()
	if !s.Enabled() {
		return rep, ErrDisabled
	}
	// encFile is an operator-supplied path from the restore CLI, not user input.
	enc, err := s.readFile(encFile)
	if err != nil {
		return rep, err
	}
	raw, err := decrypt(enc, s.secret)
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
	sortNewestFirst(out)
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
	return s.OpenContext(context.Background(), name)
}

// OpenContext reads a stored archive under the caller's memory-operation lease.
func (s *Service) OpenContext(ctx context.Context, name string) ([]byte, error) {
	_, release, err := s.AcquireWork(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
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
	return s.readArchive(f)
}

// Boot delay bounds for the scheduler's first tick (see Options.FirstTickDelay).
const (
	minBootDelay = 2 * time.Minute
	maxBootDelay = 5 * time.Minute
)

// Loop polls once a minute and runs the volume and S3 backups independently when
// each is due. Reading the schedule from the DB each tick means GUI edits apply
// without a restart, and a mere restart no longer forces a backup. The first
// tick waits out a jittered boot delay: if a run killed the previous process,
// restarting it within seconds of every boot would turn one crash into a loop.
func (s *Service) Loop(ctx context.Context, logger *slog.Logger) {
	if !s.Enabled() {
		return
	}
	if delay := s.bootDelay(); delay > 0 {
		logger.Info("backup scheduler starts after boot delay", "delay", delay.Round(time.Second).String())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	s.safeTick(ctx, logger)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.safeTick(ctx, logger)
		}
	}
}

// bootDelay returns how long Loop waits before its first tick.
func (s *Service) bootDelay() time.Duration {
	switch {
	case s.opt.FirstTickDelay < 0:
		return 0
	case s.opt.FirstTickDelay > 0:
		return s.opt.FirstTickDelay
	}
	return minBootDelay + randomDuration(maxBootDelay-minBootDelay)
}

// randomDuration returns a uniformly distributed duration in [0, bound).
func randomDuration(bound time.Duration) time.Duration {
	var b [8]byte
	if bound <= 0 {
		return 0
	}
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(bound))
}

// safeTick runs one tick and keeps a panic from killing the whole process.
func (s *Service) safeTick(ctx context.Context, logger *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("backup scheduler tick panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	s.tick(ctx, logger)
}

// tick runs due schedules only while this instance owns the scheduler lease.
func (s *Service) tick(ctx context.Context, logger *slog.Logger) {
	releaseScheduler, acquired, err := s.acquireSchedulerLease(ctx)
	if err != nil {
		logger.Error("backup scheduler lease failed", "err", err)
		return
	}
	if !acquired {
		return
	}
	defer func() {
		if err := releaseScheduler(); err != nil {
			logger.Error("backup scheduler lease release failed", "err", err)
		}
	}()
	s.removeStalePlainTemps(plainTempMaxAge)

	set := s.currentSettings(ctx)
	st := s.readStatus()
	now := time.Now()
	state, err := s.loadSchedulerState(ctx, st)
	if err != nil {
		logger.Error("backup scheduler state failed", "err", err)
		return
	}
	s.settleInterruptedAttempts(ctx, logger, state)
	// The `now.After(retryAt)` guard bounds a persistently failing destination to
	// one attempt per statusRetryBackoff instead of a heavy pg_dump every minute
	// (LastBackup/LastS3 only advance on success, so cronDue stays true meanwhile).
	// The retry clock is also set when a run starts, so a run that kills the
	// process counts as a failed attempt and is not repeated on the next boot.
	if cronDue(set.VolumeCron, state.volumeLast, now) && now.After(state.volumeRetry) {
		s.runScheduledDestination(ctx, logger, "volume", now, func(c context.Context) error {
			return s.runVolume(c, set.VolumeKeep)
		})
	}
	if s.S3Enabled() {
		if cronDue(set.S3Cron, state.s3Last, now) && now.After(state.s3Retry) {
			s.runScheduledDestination(ctx, logger, "s3", now, func(c context.Context) error {
				return s.runS3Mirror(c, set.S3Keep)
			})
		}
	}
}

// runScheduledDestination runs one due scheduled job with crash bookkeeping:
// the attempt marker is persisted first, a panic becomes a recorded failure,
// and the outcome replaces the marker with a success or a retry clock.
func (s *Service) runScheduledDestination(ctx context.Context, logger *slog.Logger,
	destination string, now time.Time, run func(context.Context) error,
) {
	if err := s.markSchedulerAttempt(ctx, destination, now); err != nil {
		// Without a durable marker a crash would be retried at once on the
		// next boot. The database is likely unavailable anyway; try later.
		s.setLocalRetry(destination, now)
		logger.Error("backup scheduler: attempt marker not saved; run skipped",
			"destination", destination, "err", err)
		return
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	runErr := s.runGuarded(destination, func() error { return run(c) })
	stateErr := s.persistSchedulerResult(ctx, destination, runErr == nil, now)
	if runErr != nil || stateErr != nil {
		s.setLocalRetry(destination, now)
		if destination == "volume" {
			logger.Error("volume backup failed", "err", errors.Join(runErr, stateErr))
		} else {
			logger.Error("s3 mirror failed", "err", errors.Join(runErr, stateErr))
		}
		return
	}
	if destination == "volume" {
		logger.Info("volume backup written", "dir", s.opt.Dir)
	} else {
		logger.Info("s3 mirror updated")
	}
}

// setLocalRetry holds a destination back for one backoff in this process.
func (s *Service) setLocalRetry(destination string, now time.Time) {
	if destination == "volume" {
		s.volRetryAt = now.Add(statusRetryBackoff)
	} else {
		s.s3RetryAt = now.Add(statusRetryBackoff)
	}
}

// runGuarded runs one scheduled job and converts a panic into a recorded
// failure (status.json and a logged stack) instead of a process crash.
func (s *Service) runGuarded(destination string, run func() error) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		slog.Error("backup: scheduled run panicked", "destination", destination,
			"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		err = errors.Join(fmt.Errorf("scheduled %s backup panicked: %v", destination, r),
			s.markDestinationFailed(destination))
	}()
	return run()
}

// markDestinationFailed records a failed run of destination in status.json.
func (s *Service) markDestinationFailed(destination string) error {
	return s.updateStatus(func(st *Status) {
		if destination == "volume" {
			st.OK = false
			return
		}
		failed := false
		st.S3OK = &failed
	})
}

// settleInterruptedAttempts turns an attempt marker left behind by a process
// that died mid-run into a recorded failure: a log line, a failed status.json
// entry and a cleared marker. The retry clock written with the marker keeps
// holding the next attempt back.
func (s *Service) settleInterruptedAttempts(ctx context.Context, logger *slog.Logger, state schedulerState) {
	for _, attempt := range []struct {
		destination string
		startedAt   time.Time
		retryAt     time.Time
	}{
		{"volume", state.volumeAttempt, state.volumeRetry},
		{"s3", state.s3Attempt, state.s3Retry},
	} {
		if attempt.startedAt.IsZero() {
			continue
		}
		metrics.Inc(metrics.BackupInterruptedRuns)
		logger.Error("backup: previous scheduled run did not finish (process stopped or crashed mid-run); counted as failed",
			"destination", attempt.destination, "started", attempt.startedAt, "next_attempt", attempt.retryAt)
		statusErr := s.markDestinationFailed(attempt.destination)
		clearErr := s.clearSchedulerAttempt(ctx, attempt.destination)
		if err := errors.Join(statusErr, clearErr); err != nil {
			logger.Error("backup: interrupted run not recorded", "destination", attempt.destination, "err", err)
		}
	}
}

// ManualVolume runs a volume backup now (the panel's scheduled-backup trigger).
func (s *Service) ManualVolume(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	err := s.runVolume(ctx, s.currentSettings(ctx).VolumeKeep)
	return errors.Join(err, s.persistSchedulerResult(ctx, "volume", err == nil, time.Now()))
}

// ManualS3 creates a fresh encrypted dump and uploads it straight to S3 now —
// the panel's "Jetzt in S3 sichern". Independent of the volume backups (works
// even when the volume schedule is off and no local dump exists) and never
// prunes. Its plaintext scratch file lives in the backup directory, so it is
// safe under a read-only root filesystem.
func (s *Service) ManualS3(ctx context.Context) (string, error) {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if !s.Enabled() {
		return "", ErrDisabled
	}
	if !s.S3Enabled() {
		return "", fmt.Errorf("S3 ist nicht konfiguriert")
	}
	// Same drill as the scheduled S3-only path: verify the dump restores before it
	// becomes an off-box copy, then re-verify the STORED object — a PUT returning nil
	// is not proof the bytes landed complete and readable off-box.
	enc, name, err := s.createVerifiedDump(ctx)
	if err != nil {
		return "", err
	}
	uerr := s.uploadVerifiedS3(ctx, name, enc)
	ok := uerr == nil
	now := time.Now()
	statusErr := s.updateStatus(func(st *Status) { st.S3OK, st.LastS3 = &ok, now })
	if uerr != nil || statusErr != nil {
		stateErr := s.persistSchedulerResult(ctx, "s3", false, now)
		return "", errors.Join(uerr, statusErr, stateErr)
	}
	if err := s.persistSchedulerResult(ctx, "s3", true, now); err != nil {
		return "", err
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

// s3Client returns the shared S3 client. minio.New builds a fresh HTTP
// transport each time, so creating one per call (and per object during
// retention) meant a new TLS session and idle connection pool every time. The
// client is rebuilt only when the connection settings change.
func (s *Service) s3Client() (*minio.Client, error) {
	o := s.opt.S3
	clientOpts := s3ClientOptions{
		Endpoint:  o.Endpoint,
		AccessKey: o.AccessKey,
		SecretKey: o.SecretKey,
		UseSSL:    o.UseSSL,
	}
	s.s3mu.Lock()
	defer s.s3mu.Unlock()
	if s.s3cl != nil && s.s3clOpts == clientOpts {
		return s.s3cl, nil
	}
	cl, err := minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	s.s3cl, s.s3clOpts = cl, clientOpts
	return cl, nil
}

// s3PreconditionFailed reports a conditional PUT that found the key taken.
func s3PreconditionFailed(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.StatusCode == 412 || resp.Code == "PreconditionFailed"
}

// uploadVerifiedS3 uploads one verified archive and proves the stored object
// is bit-identical to it. If the name is already taken (HTTP 412), the object
// is accepted only when this installation owns it and it matches exactly —
// typically an upload from an earlier run whose bookkeeping failed afterwards.
func (s *Service) uploadVerifiedS3(ctx context.Context, name string, data []byte) error {
	err := s.uploadS3(ctx, name, data)
	if err != nil && s3PreconditionFailed(err) {
		owned, ownErr := s.s3ObjectOwned(ctx, name)
		if ownErr != nil {
			return errors.Join(err, ownErr)
		}
		if !owned {
			return fmt.Errorf("s3 object %q already exists and is not owned by this installation: %w", name, err)
		}
		err = nil // verify the existing owned object below instead of failing
	}
	if err != nil {
		return err
	}
	return s.verifyS3Object(ctx, name, int64(len(data)), sha256.Sum256(data))
}

const s3OwnerMetadata = "treckrr-installation"

// s3ObjectNotFound reports the S3-compatible not-found variants returned by
// different providers for HEAD requests.
func s3ObjectNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.StatusCode == 404 || resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject"
}

// s3ReadPrefixes returns the normalized write namespace followed by the exact
// legacy concatenation prefix, when they differ.
func (s *Service) s3ReadPrefixes() []string {
	prefixes := []string{s.opt.S3.Prefix}
	if legacy := s.opt.S3.LegacyPrefix; legacy != nil && *legacy != s.opt.S3.Prefix {
		prefixes = append(prefixes, *legacy)
	}
	return prefixes
}

// s3InstallationID is a stable, non-secret ownership marker derived from the
// bucket namespace. Configuration requires the prefix to be installation-
// specific; hashing avoids copying the raw deployment name into metadata.
func (s *Service) s3InstallationID() string {
	digest := sha256.Sum256([]byte(s.opt.S3.Bucket + "\x00" + s.opt.S3.Prefix))
	return fmt.Sprintf("%x", digest[:])
}

// s3ObjectOwned verifies that an object carries this installation's immutable
// ownership marker before destructive maintenance may act on it.
func (s *Service) s3ObjectOwned(ctx context.Context, name string) (bool, error) {
	cl, err := s.s3Client()
	if err != nil {
		return false, err
	}
	info, err := cl.StatObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+name, minio.StatObjectOptions{})
	if err != nil {
		if s3ObjectNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("s3 head %q: %w", name, err)
	}
	want := s.s3InstallationID()
	for key, value := range info.UserMetadata {
		if strings.EqualFold(key, s3OwnerMetadata) {
			return value == want, nil
		}
	}
	return false, nil
}

// pruneS3 keeps only the newest owned objects in this installation's namespace
// (0 = keep all). Untagged legacy and foreign objects remain readable, but are
// never counted toward retention and never deleted automatically. Retention
// problems do not fail the mirror (the new copy is stored and verified), but
// every one is logged and counted, so a stalled retention stays visible.
// It returns the number of removed objects and of failed steps.
func (s *Service) pruneS3(ctx context.Context, keep int) (removed, failures int) {
	if keep <= 0 {
		return 0, 0
	}
	fail := func(msg string, args ...any) {
		failures++
		metrics.Inc(metrics.BackupS3PruneFailures)
		slog.Warn(msg, args...)
	}
	files, err := s.S3List(ctx)
	if err != nil {
		fail("backup prune: s3 listing failed; retention skipped", "err", err)
		return 0, failures
	}
	cl, err := s.s3Client()
	if err != nil {
		fail("backup prune: s3 client unavailable; retention skipped", "err", err)
		return 0, failures
	}
	cutoff := time.Now().Add(-minRetention)
	kept := 0
	for _, file := range files { // S3List is newest-first
		// Once keep owned copies are counted, only objects old enough to be
		// removed need an ownership check; younger ones stay either way.
		if kept >= keep && (file.ModTime.IsZero() || file.ModTime.After(cutoff)) {
			continue
		}
		isOwned, ownErr := s.s3ObjectOwned(ctx, file.Name)
		if ownErr != nil {
			fail("backup prune: s3 ownership check failed", "object", file.Name, "err", ownErr)
			continue
		}
		if !isOwned {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := cl.RemoveObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+file.Name, minio.RemoveObjectOptions{}); err != nil {
			fail("backup prune: s3 remove failed", "object", file.Name, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 || failures > 0 {
		slog.Info("backup prune: s3 retention applied", "removed", removed, "failures", failures, "keep", keep)
	}
	return removed, failures
}

// uploadS3 puts one encrypted dump into the configured S3-compatible bucket, for
// the 3-2-1 rule (a copy that does not sit next to the DB it protects).
func (s *Service) uploadS3(ctx context.Context, name string, data []byte) error {
	cl, err := s.s3Client()
	if err != nil {
		return err
	}
	opts := minio.PutObjectOptions{
		ContentType:  "application/octet-stream",
		UserMetadata: map[string]string{s3OwnerMetadata: s.s3InstallationID()},
	}
	// A collision must fail instead of overwriting an object that another
	// process or installation may have created.
	opts.SetMatchETagExcept("*")
	_, err = cl.PutObject(ctx, s.opt.S3.Bucket, s.opt.S3.Prefix+name, bytes.NewReader(data), int64(len(data)),
		opts)
	return err
}

// verifyS3Object re-reads a stored object and proves it is bit-identical to a
// local archive that already passed the full drill (decrypt + pg_restore --list
// + content sanity): same size and same SHA-256 (wantSum) of the encrypted
// bytes. That proves the off-box copy is intact, not merely that PutObject
// returned. The read-back is streamed through the hash instead of buffered, so
// verifying costs no extra archive-sized allocation.
// Do NOT compare the ETag to an md5 — a multipart upload's ETag is not the object md5.
func (s *Service) verifyS3Object(ctx context.Context, name string, wantSize int64, wantSum [32]byte) error {
	if wantSize <= 0 || wantSize > s.maxBytes() {
		return fmt.Errorf("backup object size out of bounds: %d", wantSize)
	}
	cl, err := s.s3Client()
	if err != nil {
		return err
	}
	key := s.opt.S3.Prefix + name
	info, err := cl.StatObject(ctx, s.opt.S3.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 head: %w", err)
	}
	owned := false
	wantOwner := s.s3InstallationID()
	for key, value := range info.UserMetadata {
		if strings.EqualFold(key, s3OwnerMetadata) && value == wantOwner {
			owned = true
			break
		}
	}
	if !owned {
		return fmt.Errorf("s3 object %q is legacy or belongs to another installation", name)
	}
	if info.Size != wantSize {
		return fmt.Errorf("s3 stored %d bytes, expected %d (incomplete upload?)", info.Size, wantSize)
	}
	obj, err := cl.GetObject(ctx, s.opt.S3.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 read-back: %w", err)
	}
	defer obj.Close()
	// Hash exactly the size we uploaded (+1 guards against an over-long object).
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(obj, wantSize+1))
	if err != nil {
		return fmt.Errorf("s3 read-back: %w", err)
	}
	if n != wantSize {
		return fmt.Errorf("s3 read-back size %d != expected %d", n, wantSize)
	}
	if !bytes.Equal(h.Sum(nil), wantSum[:]) {
		return fmt.Errorf("s3 object %q differs from the verified local archive", name)
	}
	return nil
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

// Bounds so a bucket writer or a compromised S3 endpoint can't exhaust app memory
// (SH-06): a cap on how many objects a listing accumulates, and a byte ceiling on
// a single downloaded object (well above any realistic encrypted dump).
const (
	maxS3Listing     = 10_000
	maxS3ObjectBytes = 128 << 20 // matches the online restore/container budget
)

// S3List returns the encrypted dumps stored in the bucket (the bucket explorer).
func (s *Service) S3List(ctx context.Context) ([]BackupFile, error) {
	if !s.S3Enabled() {
		return nil, fmt.Errorf("S3 ist nicht konfiguriert")
	}
	cl, err := s.s3Client()
	if err != nil {
		return nil, err
	}
	// Bound each namespace's listing by objects seen (not just matches), so a
	// bucket full of non-matching keys can't drive unbounded iteration. Over the
	// cap we refuse with an error rather than return a partial list; canceling
	// the context stops the lister.
	//
	// Every listing is narrowed to "<namespace>treckrr-", the only keys that can
	// hold a backup. Without that, a legacy prefix without a trailing slash
	// ("farm-a") also enumerated the whole normalized namespace ("farm-a/…") and
	// sibling installations ("farm-ab/…"), counting them against the cap.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var out []BackupFile
	names := make(map[string]struct{})
	prefixes := s.s3ReadPrefixes()
	for i, prefix := range prefixes {
		seen := 0
		for obj := range cl.ListObjects(lctx, s.opt.S3.Bucket,
			minio.ListObjectsOptions{Prefix: prefix + "treckrr-", Recursive: true}) {
			if obj.Err != nil {
				return nil, obj.Err
			}
			if !strings.HasPrefix(obj.Key, prefix) || coveredByPrefix(obj.Key, prefixes[:i]) {
				continue // outside this namespace, or already listed under an earlier one
			}
			if seen++; seen > maxS3Listing {
				return nil, fmt.Errorf("S3 listing exceeds %d objects; refusing to enumerate unbounded", maxS3Listing)
			}
			name := strings.TrimPrefix(obj.Key, prefix)
			if !validName(name) {
				continue
			}
			if _, exists := names[name]; exists {
				continue
			}
			names[name] = struct{}{}
			out = append(out, BackupFile{Name: name, Size: obj.Size, ModTime: obj.LastModified})
		}
	}
	sortNewestFirst(out)
	return out, nil
}

// coveredByPrefix reports whether key belongs to one of the given namespaces.
func coveredByPrefix(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// s3ReadableObject locates a backup in the normalized namespace first, then in
// the exact legacy concatenation namespace used by earlier releases.
func (s *Service) s3ReadableObject(ctx context.Context, cl *minio.Client, name string) (string, minio.ObjectInfo, error) {
	var notFound error
	for _, prefix := range s.s3ReadPrefixes() {
		key := prefix + name
		info, err := cl.StatObject(ctx, s.opt.S3.Bucket, key, minio.StatObjectOptions{})
		if err == nil {
			return key, info, nil
		}
		if !s3ObjectNotFound(err) {
			return "", minio.ObjectInfo{}, err
		}
		notFound = err
	}
	return "", minio.ObjectInfo{}, notFound
}

// S3Get downloads one object from the bucket (still encrypted).
func (s *Service) S3Get(ctx context.Context, name string) ([]byte, error) {
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
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
	key, info, err := s.s3ReadableObject(ctx, cl, name)
	if err != nil {
		return nil, err
	}
	// Reject an oversized object up front (SH-06) so a compromised endpoint can't
	// exhaust memory before the read.
	if info.Size < 0 || info.Size > s.maxBytes() {
		return nil, fmt.Errorf("backup object too large: %d bytes", info.Size)
	}
	obj, err := cl.GetObject(ctx, s.opt.S3.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	// Belt-and-suspenders: cap the read even if the reported size lied.
	data, err := io.ReadAll(io.LimitReader(obj, s.maxBytes()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxBytes() {
		return nil, fmt.Errorf("backup object exceeds %d bytes", s.maxBytes())
	}
	return data, nil
}

// writeStatus durably replaces the local scheduler status document.
func (s *Service) writeStatus(st Status) error {
	if s.opt.StatusFile == "" {
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal backup status: %w", err)
	}
	if err := writeFileAtomic(s.opt.StatusFile, b); err != nil {
		return fmt.Errorf("write backup status: %w", err)
	}
	return nil
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
		_ = os.Remove(tmp) // #nosec G703 -- temp name from os.CreateTemp in the target dir
		return err
	}
	if err := f.Sync(); err != nil { // flush to disk before the rename
		_ = f.Close()
		_ = os.Remove(tmp) // #nosec G703 -- temp name from os.CreateTemp in the target dir
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp) // #nosec G703 -- temp name from os.CreateTemp in the target dir
		return err
	}
	if err := durableRename(tmp, path); err != nil {
		_ = os.Remove(tmp) // #nosec G703 -- temp name from os.CreateTemp in the target dir
		return err
	}
	return nil
}

// writeTemp writes raw dump bytes to a private scratch file for pg_restore
// (which needs a seekable archive) and returns a cleanup func. See
// createScratch for why this is not the RAM-backed /tmp.
func (s *Service) writeTemp(raw []byte) (path string, cleanup func(), err error) {
	f, err := s.createScratch()
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
