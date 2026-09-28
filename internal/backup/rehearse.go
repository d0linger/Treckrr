package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrRehearsalDisabled is returned when a restore rehearsal is requested but no
// rehearsal database URL is configured.
var ErrRehearsalDisabled = errors.New("restore rehearsal is not configured")

// rehearsalTimeout bounds one rehearsal end to end. A restore of this app's data
// is seconds; the ceiling exists so a hung pg_restore cannot pin the operation
// semaphore (and with it every scheduled backup) indefinitely.
const rehearsalTimeout = 10 * time.Minute

// RehearseRestore proves a dump can actually be restored, by restoring it into a
// scratch database and querying the result.
//
// The distinction matters: ValidateArchive only reads the archive's table of
// contents (pg_restore --list) and sanity-checks the object names. That proves
// the file is a well-formed archive — it does NOT prove pg_restore can load it,
// which is the thing "restore tested" is supposed to mean. Everything a real
// restore can trip over (a type the server lacks, an extension, an incompatible
// server version, a truncated data section) is invisible to the TOC read.
//
// The scratch database is created and dropped by this function and is never the
// live one: the name is derived here, and a rehearsal URL that resolves to the
// same database as opt.DatabaseURL is refused outright.
func (s *Service) RehearseRestore(ctx context.Context, enc []byte) (Rehearsal, error) {
	var rep Rehearsal
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return rep, err
	}
	defer release()
	if s.opt.RehearseURL == "" {
		return rep, ErrRehearsalDisabled
	}
	if int64(len(enc)) > s.maxBytes() {
		return rep, errors.New("rehearsal archive exceeds configured memory budget")
	}
	raw, err := decrypt(enc, s.secret)
	if err != nil {
		return rep, fmt.Errorf("decrypt: %w", err)
	}

	adminURL, scratch, err := scratchTarget(s.opt.RehearseURL, s.opt.DatabaseURL)
	if err != nil {
		return rep, err
	}

	ctx, cancel := context.WithTimeout(ctx, rehearsalTimeout)
	defer cancel()

	// Serialize with dumps and restores: a rehearsal runs pg_restore and must not
	// overlap the scheduled backup that produced the very file it is checking.
	if err := s.acquireOp(ctx); err != nil {
		return rep, err
	}
	defer s.releaseOp()

	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		return rep, fmt.Errorf("rehearsal: connect: %w", err)
	}
	defer admin.Close()

	// An interrupted earlier rehearsal (SIGKILL, OOM, a crashed host) leaves a
	// full plaintext copy of production behind. Remove such leftovers first.
	dropStaleScratch(ctx, admin, time.Now())

	// Never drop an existing database to recover from a name collision.
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+scratch+`"`); err != nil {
		return rep, fmt.Errorf("rehearsal: create scratch db: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		dropScratch(cleanupCtx, admin, scratch)
	}()

	tmp, cleanup, err := s.writeTemp(raw)
	if err != nil {
		return rep, err
	}
	defer cleanup()

	targetURL := withDatabase(s.opt.RehearseURL, scratch)
	dbURL, env, err := dbURLEnv(targetURL)
	if err != nil {
		return rep, err
	}
	start := time.Now()
	// No --clean here: the database is empty by construction. --single-transaction
	// still applies, so a partial load cannot be mistaken for a success.
	cmd := exec.CommandContext(ctx, "pg_restore", // #nosec G204
		"--no-owner", "--single-transaction", "--dbname="+dbURL, tmp)
	cmd.Env = env
	var errBuf bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &errBuf, remaining: 1 << 20}
	if err := cmd.Run(); err != nil {
		return rep, fmt.Errorf("rehearsal: pg_restore: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}

	// The load succeeded — now prove the result is a usable Treckrr database and
	// not an empty shell that pg_restore happened to accept.
	probe, err := sql.Open("pgx", targetURL)
	if err != nil {
		return rep, fmt.Errorf("rehearsal: connect scratch: %w", err)
	}
	defer probe.Close()
	if err := probe.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&rep.Migrations); err != nil {
		return rep, fmt.Errorf("rehearsal: schema_migrations unreadable: %w", err)
	}
	if rep.Migrations == 0 {
		return rep, errors.New("rehearsal: restored database has no applied migrations")
	}
	if err := probe.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&rep.Tables); err != nil {
		return rep, fmt.Errorf("rehearsal: table count: %w", err)
	}
	// The three tables that carry the money. A dump that restores but lost these
	// is not a recovery point.
	for _, t := range []string{"entries", "payments", "invoices"} {
		var n int64
		if err := probe.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			return rep, fmt.Errorf("rehearsal: %s unreadable after restore: %w", t, err)
		}
		rep.Rows += n
	}
	rep.Duration = time.Since(start)
	rep.At = time.Now()
	if err := s.recordRehearsal(rep); err != nil {
		return rep, fmt.Errorf("rehearsal succeeded but status was not saved: %w", err)
	}
	return rep, nil
}

func (s *Service) recordRehearsal(rep Rehearsal) error {
	if s.opt.StatusFile == "" {
		return nil // callers without a status destination receive the report only
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.readStatus()
	st.RestoreTested = rep.At
	st.RehearsalNote = fmt.Sprintf("Restored and queried %d tables in %s", rep.Tables, rep.Duration.Round(time.Millisecond))
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.opt.StatusFile, b)
}

// Rehearsal is the result of a successful restore rehearsal.
type Rehearsal struct {
	At         time.Time
	Duration   time.Duration
	Migrations int64
	Tables     int64
	Rows       int64
}

func dropScratch(ctx context.Context, admin *sql.DB, name string) {
	// FORCE terminates leftover connections; without it a dangling probe
	// connection makes the drop fail and the scratch database accumulates.
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
		slog.Warn("rehearsal: scratch database not dropped", "db", name, "err", err)
	}
}

// scratchPrefix names every rehearsal scratch database.
const scratchPrefix = "treckrr_rehearsal_"

// scratchName builds "treckrr_rehearsal_<unix seconds>_<nonce>" (61 bytes with
// a 32-hex nonce, inside PostgreSQL's 63-byte identifier limit).
func scratchName(created time.Time, nonce string) string {
	return scratchPrefix + strconv.FormatInt(created.Unix(), 10) + "_" + nonce
}

// staleScratchAge is how old a scratch database must be before another
// rehearsal may drop it: older than any rehearsal can legitimately run.
const staleScratchAge = rehearsalTimeout + 5*time.Minute

// staleScratch decides whether a scratch database is a leftover. Timestamped
// names are stale once older than staleScratchAge. Names from earlier releases
// (a bare 32-hex nonce, no timestamp) cannot be dated; they are only dropped
// when no session is connected to them, since a running rehearsal holds one
// for all but a moment.
func staleScratch(name string, connected bool, now time.Time) bool {
	rest, ok := strings.CutPrefix(name, scratchPrefix)
	if !ok {
		return false
	}
	stamp, nonce, timestamped := strings.Cut(rest, "_")
	if !timestamped {
		return isHex(rest, 32) && !connected
	}
	secs, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || !isHex(nonce, 32) {
		return false
	}
	return now.Sub(time.Unix(secs, 0)) > staleScratchAge
}

// isHex reports whether s is exactly n lowercase hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// dropStaleScratch removes leftover rehearsal databases. Best effort: a
// failure is logged and never blocks the rehearsal itself.
func dropStaleScratch(ctx context.Context, admin *sql.DB, now time.Time) {
	stale, err := listStaleScratch(ctx, admin, now)
	if err != nil {
		slog.Warn("rehearsal: listing leftover scratch databases failed", "err", err)
	}
	for _, name := range stale {
		slog.Warn("rehearsal: dropping leftover scratch database from an interrupted run", "db", name)
		dropScratch(ctx, admin, name)
	}
}

// listStaleScratch returns the rehearsal scratch databases that staleScratch
// classifies as leftovers.
func listStaleScratch(ctx context.Context, admin *sql.DB, now time.Time) ([]string, error) {
	rows, err := admin.QueryContext(ctx, `
		SELECT d.datname,
		       EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname = d.datname)
		  FROM pg_database d
		 WHERE d.datname LIKE 'treckrr\_rehearsal\_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var name string
		var connected bool
		if err := rows.Scan(&name, &connected); err != nil {
			return stale, err
		}
		if staleScratch(name, connected, now) {
			stale = append(stale, name)
		}
	}
	return stale, rows.Err()
}

// scratchTarget derives the maintenance connection (to the "postgres" database)
// and the scratch database name, and refuses a configuration that would point
// the rehearsal at the live database.
func scratchTarget(rehearseURL, liveURL string) (adminURL, scratch string, err error) {
	ru, err := url.Parse(rehearseURL)
	if err != nil || (ru.Scheme != "postgres" && ru.Scheme != "postgresql") {
		return "", "", errors.New("rehearsal: a valid PostgreSQL URL is required")
	}
	// The prefix stays recognizable; ownership is unique to this invocation.
	// The creation time in the name lets a later rehearsal recognize and drop
	// a leftover that an interrupted run could not clean up itself.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", err
	}
	scratch = scratchName(time.Now(), hex.EncodeToString(nonce[:]))
	if lu, lerr := url.Parse(liveURL); lerr == nil {
		if strings.EqualFold(lu.Host, ru.Host) && strings.TrimPrefix(lu.Path, "/") == scratch {
			return "", "", errors.New("rehearsal: the rehearsal database must not be the live database")
		}
	}
	// CREATE/DROP DATABASE must use the maintenance DB. Query-string dbname
	// must not override the path and silently target an operator's existing DB.
	return withDatabase(rehearseURL, "postgres"), scratch, nil
}

// withDatabase replaces the path and database aliases without re-encoding
// credentials or unrelated options used by the restore rehearsal.
func withDatabase(rawURL, dbName string) string {
	u, err := parseDatabaseURI(rawURL)
	if err != nil {
		return rawURL
	}
	u.path = "/" + url.PathEscape(dbName)
	params := make([]databaseURIParam, 0, len(u.params)+1)
	for _, param := range u.params {
		if param.key != "dbname" && param.key != "database" {
			params = append(params, param)
		}
	}
	// pgx accepts both aliases; neither may override the scratch target.
	params = append(params, databaseURIParam{raw: "dbname=" + url.PathEscape(dbName)})
	u.params = params
	return u.String()
}
