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

	// Never drop an existing database to recover from a name collision.
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+scratch+`"`); err != nil {
		return rep, fmt.Errorf("rehearsal: create scratch db: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		dropScratch(cleanupCtx, admin, scratch)
	}()

	tmp, cleanup, err := writeTemp(raw)
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

// scratchTarget derives the maintenance connection (to the "postgres" database)
// and the scratch database name, and refuses a configuration that would point
// the rehearsal at the live database.
func scratchTarget(rehearseURL, liveURL string) (adminURL, scratch string, err error) {
	ru, err := url.Parse(rehearseURL)
	if err != nil || (ru.Scheme != "postgres" && ru.Scheme != "postgresql") {
		return "", "", errors.New("rehearsal: a valid PostgreSQL URL is required")
	}
	// The prefix stays recognizable; ownership is unique to this invocation.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", err
	}
	scratch = "treckrr_rehearsal_" + hex.EncodeToString(nonce[:])
	if lu, lerr := url.Parse(liveURL); lerr == nil {
		if strings.EqualFold(lu.Host, ru.Host) && strings.TrimPrefix(lu.Path, "/") == scratch {
			return "", "", errors.New("rehearsal: the rehearsal database must not be the live database")
		}
	}
	// CREATE/DROP DATABASE must use the maintenance DB. Query-string dbname
	// must not override the path and silently target an operator's existing DB.
	return withDatabase(rehearseURL, "postgres"), scratch, nil
}

func withDatabase(rawURL, dbName string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Path = "/" + dbName
	q := u.Query()
	q.Set("dbname", dbName)
	u.RawQuery = q.Encode()
	return u.String()
}
