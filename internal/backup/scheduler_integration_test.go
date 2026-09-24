//go:build integration

package backup

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	appdb "github.com/d0linger/treckrr/internal/db"
)

func TestSchedulerLeaseAndStateAreClusterWide(t *testing.T) {
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
	one := New(Options{}, pool)
	two := New(Options{}, pool)
	release, acquired, err := one.acquireSchedulerLease(t.Context())
	if err != nil || !acquired {
		t.Fatalf("first scheduler lease = %v, %v", acquired, err)
	}
	if otherRelease, acquired, err := two.acquireSchedulerLease(t.Context()); err != nil || acquired {
		if acquired {
			_ = otherRelease()
		}
		t.Fatalf("second scheduler lease = %v, %v; want busy", acquired, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	var oldLast, oldRetry sql.NullTime
	if err := pool.QueryRowContext(t.Context(), `
		SELECT volume_last_success, volume_retry_at
		  FROM backup_scheduler_state WHERE id=1`).Scan(&oldLast, &oldRetry); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET volume_last_success=$1, volume_retry_at=$2, updated_at=now()
			 WHERE id=1`, nullableTime(oldLast), nullableTime(oldRetry))
	})
	want := time.Now().UTC().Truncate(time.Microsecond)
	if err := one.recordSchedulerResult(t.Context(), "volume", true, want); err != nil {
		t.Fatal(err)
	}
	state, err := two.loadSchedulerState(t.Context(), Status{})
	if err != nil {
		t.Fatal(err)
	}
	if !state.volumeLast.Equal(want) || !state.volumeRetry.IsZero() {
		t.Fatalf("shared state = %#v, want last=%v and no retry", state, want)
	}
}

func nullableTime(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time
}
