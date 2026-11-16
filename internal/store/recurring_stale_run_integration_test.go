//go:build integration

package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

// TestRecurringChangeStopsSelectedRunIntegration pins the runner against the
// operator: RunDueRecurring selects due rules before it books, so a pause,
// reschedule or delete can complete in between. Once it has, the selected
// occurrence must not be booked (Codex audit 2026-11,
// recurring/stale-run-after-lifecycle-change).
func TestRecurringChangeStopsSelectedRunIntegration(t *testing.T) {
	// "cadence" keeps next_run and changes only the interval (CodeRabbit
	// 2026-11-16): the runner must not book on with the old cadence.
	for _, change := range []string{"pause", "reschedule", "cadence", "delete"} {
		t.Run(change, func(t *testing.T) {
			ctx := t.Context()
			st, pool, yearID, neighborID := scratchBookingFixtureYear(t, time.Now().Year())
			today := time.Now().Format("2006-01-02")
			var ruleID int64
			if err := pool.QueryRowContext(ctx, `INSERT INTO recurring_entries
				(neighbor_id,template,interval_kind,next_run) VALUES
				($1,'{"unit":"h","hours":"1","hourly_rate":"10","cost":"10","task_label":"Stale run"}'::jsonb,'weekly',$2)
				RETURNING id`, neighborID, today).Scan(&ruleID); err != nil {
				t.Fatal(err)
			}
			// Hold the booking year's lock so the runner stops after selecting
			// the rule and before its booking transaction can proceed.
			holder, err := pool.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback() //nolint:errcheck
			var holderPID int
			if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
				t.Fatal(err)
			}
			type result struct {
				created int
				err     error
			}
			done := make(chan result, 1)
			go func() {
				created, err := st.RunDueRecurring(ctx)
				done <- result{created: created, err: err}
			}()
			waitForDatabaseBlock(t, ctx, pool, holderPID)
			later := time.Now().AddDate(0, 0, 14)
			switch change {
			case "pause":
				if active, err := st.ToggleRecurring(ctx, ruleID); err != nil || active {
					t.Fatalf("pause while the runner waits: active=%v err=%v", active, err)
				}
			case "reschedule":
				if err := st.UpdateRecurringSchedule(ctx, ruleID, "weekly", later, nil); err != nil {
					t.Fatalf("reschedule while the runner waits: %v", err)
				}
			case "cadence":
				if err := st.UpdateRecurringSchedule(ctx, ruleID, "monthly", time.Now(), nil); err != nil {
					t.Fatalf("cadence change while the runner waits: %v", err)
				}
			case "delete":
				if _, err := st.DeleteRecurring(ctx, ruleID); err != nil {
					t.Fatalf("delete while the runner waits: %v", err)
				}
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil || got.created != 0 {
				t.Fatalf("runner after %s: created=%d err=%v, want 0/nil", change, got.created, got.err)
			}
			var entries int
			if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE idempotency_key=$1`,
				fmt.Sprintf("recur:%d:%s", ruleID, today)).Scan(&entries); err != nil {
				t.Fatal(err)
			}
			if entries != 0 {
				t.Fatalf("%s rule still booked %d occurrence(s)", change, entries)
			}
			var active bool
			var next time.Time
			var kind string
			err = pool.QueryRowContext(ctx, `SELECT active, next_run, interval_kind FROM recurring_entries WHERE id=$1`, ruleID).Scan(&active, &next, &kind)
			switch {
			case change == "delete":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("deleted rule: %v", err)
				}
			case err != nil:
				t.Fatal(err)
			case change == "pause" && (active || next.Format("2006-01-02") != today):
				t.Fatalf("paused rule: active=%v next=%s, want paused at %s", active, next.Format("2006-01-02"), today)
			case change == "reschedule" && next.Format("2006-01-02") != later.Format("2006-01-02"):
				t.Fatalf("rescheduled rule: next=%s, want the operator's %s", next.Format("2006-01-02"), later.Format("2006-01-02"))
			case change == "cadence" && (kind != "monthly" || next.Format("2006-01-02") != today):
				t.Fatalf("cadence change: kind=%s next=%s, want monthly at %s", kind, next.Format("2006-01-02"), today)
			}
			if change == "pause" {
				// "Jetzt buchen" on the paused rule is refused as before.
				if _, _, err := st.RunRecurringNow(ctx, ruleID); !errors.Is(err, store.ErrInactiveRule) {
					t.Fatalf("run-now on a paused rule = %v, want ErrInactiveRule", err)
				}
			}
		})
	}
}

// TestRecurringWaitReasonDroppedAfterRescheduleIntegration: a runner that finds
// no open year for the date it selected must not stamp that waiting reason
// onto a rule the operator rescheduled meanwhile (CodeRabbit 2026-11-16).
func TestRecurringWaitReasonDroppedAfterRescheduleIntegration(t *testing.T) {
	ctx := t.Context()
	st, pool, _, neighborID := scratchBookingFixtureYear(t, time.Now().Year())
	// The fixture has a billing year only for the current year.
	stale := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	var ruleID int64
	if err := pool.QueryRowContext(ctx, `INSERT INTO recurring_entries
		(neighbor_id,template,interval_kind,next_run) VALUES
		($1,'{"unit":"h","hours":"1","hourly_rate":"10","cost":"10","task_label":"Waiting run"}'::jsonb,'weekly',$2)
		RETURNING id`, neighborID, stale).Scan(&ruleID); err != nil {
		t.Fatal(err)
	}
	// The open-year lookup reads billing_years; holding it (in this test's own
	// scratch database) parks the runner after it selected the rule.
	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback() //nolint:errcheck
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `LOCK TABLE billing_years IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		created int
		err     error
	}
	done := make(chan result, 1)
	go func() {
		created, err := st.RunDueRecurring(ctx)
		done <- result{created: created, err: err}
	}()
	waitForDatabaseBlock(t, ctx, pool, holderPID)
	later := time.Now().AddDate(0, 0, 14)
	if err := st.UpdateRecurringSchedule(ctx, ruleID, "weekly", later, nil); err != nil {
		t.Fatalf("reschedule while the runner waits: %v", err)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || got.created != 0 {
		t.Fatalf("runner: created=%d err=%v, want 0/nil", got.created, got.err)
	}
	var lastError sql.NullString
	var next time.Time
	if err := pool.QueryRowContext(ctx, `SELECT last_error, next_run FROM recurring_entries WHERE id=$1`, ruleID).Scan(&lastError, &next); err != nil {
		t.Fatal(err)
	}
	if lastError.String != "" || next.Format("2006-01-02") != later.Format("2006-01-02") {
		t.Fatalf("rescheduled rule: last_error=%q next=%s, want no stale reason and next %s",
			lastError.String, next.Format("2006-01-02"), later.Format("2006-01-02"))
	}
}
