//go:build integration

package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	appdb "github.com/d0linger/treckrr/internal/db"
)

// TestInterruptedScheduledRunCountsAsFailure simulates a process that died
// mid-backup: its attempt marker must hold the next run back and be recorded
// as a failure by the next scheduler tick instead of restarting the job.
func TestInterruptedScheduledRunCountsAsFailure(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := appdb.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := appdb.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var oldLast, oldRetry, oldAttempt sql.NullTime
	if err := pool.QueryRowContext(t.Context(), `
		SELECT volume_last_success, volume_retry_at, volume_attempt_at
		  FROM backup_scheduler_state WHERE id=1`).Scan(&oldLast, &oldRetry, &oldAttempt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET volume_last_success=$1, volume_retry_at=$2, volume_attempt_at=$3, updated_at=now()
			 WHERE id=1`, nullableTime(oldLast), nullableTime(oldRetry), nullableTime(oldAttempt))
	})

	// The dying process marked its attempt before starting the heavy work.
	started := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	dead := New(Options{}, pool)
	if err := dead.markSchedulerAttempt(t.Context(), "volume", started); err != nil {
		t.Fatal(err)
	}

	// The restarted process sees the marker on its next tick.
	status := filepath.Join(t.TempDir(), "status.json")
	next := New(Options{StatusFile: status}, pool)
	if err := next.updateStatus(func(st *Status) { st.OK = true }); err != nil {
		t.Fatal(err)
	}
	state, err := next.loadSchedulerState(t.Context(), Status{})
	if err != nil {
		t.Fatal(err)
	}
	if !state.volumeAttempt.Equal(started) || !state.volumeRetry.Equal(started.Add(statusRetryBackoff)) {
		t.Fatalf("state after interrupted run = %+v", state)
	}
	if cronDue("* * * * *", state.volumeLast, time.Now()) && time.Now().After(state.volumeRetry) {
		t.Fatal("an interrupted run would be restarted immediately")
	}
	next.settleInterruptedAttempts(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), state)
	if next.readStatus().OK {
		t.Fatal("interrupted run not recorded as failed in status.json")
	}
	after, err := next.loadSchedulerState(t.Context(), Status{})
	if err != nil {
		t.Fatal(err)
	}
	if !after.volumeAttempt.IsZero() || !after.volumeRetry.Equal(state.volumeRetry) {
		t.Fatalf("marker not settled or retry lost: %+v", after)
	}

	// A completed run clears both the marker and the retry clock.
	if err := next.markSchedulerAttempt(t.Context(), "volume", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := next.recordSchedulerResult(t.Context(), "volume", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	done, err := next.loadSchedulerState(t.Context(), Status{})
	if err != nil {
		t.Fatal(err)
	}
	if !done.volumeAttempt.IsZero() || !done.volumeRetry.IsZero() {
		t.Fatalf("successful run left bookkeeping behind: %+v", done)
	}
}

// TestStaleRehearsalDatabasesAreFound verifies an interrupted rehearsal's
// scratch database is recognized as a leftover and a fresh one is not.
func TestStaleRehearsalDatabasesAreFound(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	adminURL, _, err := scratchTarget(dsn, "postgres://unused/live")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	nonce := func() string {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(b[:])
	}
	now := time.Now()
	stale := scratchName(now.Add(-staleScratchAge-time.Hour), nonce())
	fresh := scratchName(now, nonce())
	for _, name := range []string{stale, fresh} {
		if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dropScratch(ctx, admin, name)
		})
	}
	found, err := listStaleScratch(t.Context(), admin, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(found, stale) || slices.Contains(found, fresh) {
		t.Fatalf("stale scratch databases = %v (stale %s, fresh %s)", found, stale, fresh)
	}
}
