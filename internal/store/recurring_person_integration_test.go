package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// A series that was set up from a booking made together with a helper repeats
// the PAIR, not half of it — and the runner survives a helper record that is
// gone by the time the occurrence comes due (booking the machine half rather
// than failing the whole maintenance run on a foreign key).
//
// This test deliberately owns the CURRENT calendar year: RunDueRecurring books
// an occurrence only into a non-completed billing year whose year matches the
// occurrence's date, so the far-future fixture years the rest of the suite uses
// can never reach the booking path at all — which is why the existing server
// test could only assert the "no open year" refusal. billing_years.year is
// UNIQUE, so the year is purged before seeding as well as after, exactly like
// every other fixture in this package.
func TestRecurringWithPersonIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yr := time.Now().Year()
	nbName := fmt.Sprintf("Serien Nachbar %d", os.Getpid())
	liveName := fmt.Sprintf("Serien Helfer %d", os.Getpid())
	goneName := fmt.Sprintf("Serien Ex-Helfer %d", os.Getpid())
	f := fixtures{Years: []int{yr}, NeighborNames: []string{nbName}}
	purgePersons := func() {
		_, _ = pool.ExecContext(ctx, `DELETE FROM persons WHERE name = ANY($1)`, []string{liveName, goneName})
	}
	purgeFixtures(t, ctx, pool, f)
	purgePersons()
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f); purgePersons() })

	baseID, err := st.CreateEmptyBase(ctx, yr, "Serien-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Serien-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, nbName, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}
	livePID, err := st.CreatePerson(ctx, liveName, dec("30"), "")
	if err != nil {
		t.Fatalf("person: %v", err)
	}
	// A helper who is removed once nothing references them any more — a series
	// template can outlive that, which is the case the runner has to survive.
	gonePID, err := st.CreatePerson(ctx, goneName, dec("30"), "")
	if err != nil {
		t.Fatalf("person: %v", err)
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	source := func(label string, personID int64) int64 {
		entry := &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: today, TaskLabel: label,
			Unit: "h", Hours: dec("2"), HourlyRate: dec("40"), Cost: dec("80.00"),
		}
		var id int64
		var cerr error
		if personID == 0 {
			id, cerr = st.CreateEntry(ctx, entry, nil)
		} else {
			id, _, cerr = st.CreateEntryPair(ctx, entry, nil, &models.Entry{
				NeighborID: nid, BillingYearID: yearID, Date: today,
				Unit: models.UnitMannstunde, Quantity: dec("2"), UnitPrice: dec("30"),
				PersonID: &personID,
			})
		}
		if cerr != nil {
			t.Fatalf("source entry: %v", cerr)
		}
		return id
	}
	tmpl := func(personID int64, name string) models.RecurTemplate {
		return models.RecurTemplate{
			Unit: "h", Hours: dec("2"), HourlyRate: dec("40"), Cost: dec("80.00"),
			TaskLabel: "Mähen",
			Companion: &models.RecurCompanion{PersonID: personID, Name: name, Rate: dec("30")},
		}
	}
	sourceA := source("Mähen A", livePID)
	if err := st.CreateRecurring(ctx, sourceA, nid, tmpl(livePID, liveName), "weekly", today); err != nil {
		t.Fatalf("rule A: %v", err)
	}
	sourceB := source("Mähen B", gonePID)
	if err := st.CreateRecurring(ctx, sourceB, nid, tmpl(gonePID, goneName), "weekly", today); err != nil {
		t.Fatalf("rule B: %v", err)
	}
	partnerB, err := st.LinkedPartnerID(ctx, sourceB)
	if err != nil {
		t.Fatalf("source companion: %v", err)
	}
	if err := st.DeleteEntryPair(ctx, sourceB, partnerB); err != nil {
		t.Fatalf("delete source pair: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM persons WHERE id=$1`, gonePID); err != nil {
		t.Fatalf("delete person: %v", err)
	}
	// Rule C is a series made FROM a Mannstunden booking: it carries its own
	// attribution rather than a companion, for a helper who is gone by now. The
	// occurrence must still be booked, attribution dropped — entries.person_id
	// is a real foreign key, so a stale id would fail the insert and, since the
	// runner returns on that error, take every rule behind it down with it on
	// every tick until someone noticed.
	tmplC := models.RecurTemplate{
		Unit: models.UnitMannstunde, Quantity: dec("2"), UnitPrice: dec("30"), Cost: dec("60.00"),
		TaskLabel: "Mannstunden " + goneName, PersonID: &gonePID,
	}
	if err := st.CreateRecurring(ctx, source("Mannstunden alt", 0), nid, tmplC, "weekly", today); err != nil {
		t.Fatalf("rule C: %v", err)
	}

	// The suite shares one database, so other rules may be due as well — assert
	// on this neighbor's rows, not on the global count.
	if _, err := st.RunDueRecurring(ctx); err != nil {
		t.Fatalf("run due: %v", err)
	}

	// Companions are the linked halves; a series made from a Mannstunden booking
	// generates an unlinked one, which is counted separately.
	generated := func() (machines, companions, solo int) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE unit = 'h'),
			       count(*) FILTER (WHERE unit = 'Mannstunde' AND linked_entry_id IS NOT NULL),
			       count(*) FILTER (WHERE unit = 'Mannstunde' AND linked_entry_id IS NULL)
			  FROM entries
			 WHERE neighbor_id=$1 AND billing_year_id=$2 AND idempotency_key LIKE 'recur:%'`,
			nid, yearID).Scan(&machines, &companions, &solo); err != nil {
			t.Fatalf("count generated: %v", err)
		}
		return
	}
	machines, companions, solo := generated()
	if solo != 1 {
		t.Fatalf("generated %d unlinked Mannstunden bookings, want 1 — the rule whose own helper is gone must still book", solo)
	}
	var soloPerson *int64
	if err := pool.QueryRowContext(ctx, `
		SELECT person_id FROM entries
		 WHERE neighbor_id=$1 AND billing_year_id=$2 AND unit='Mannstunde'
		   AND linked_entry_id IS NULL AND idempotency_key LIKE 'recur:%'`,
		nid, yearID).Scan(&soloPerson); err != nil {
		t.Fatalf("read unlinked booking: %v", err)
	}
	if soloPerson != nil {
		t.Errorf("booking kept the attribution %d of a deleted helper", *soloPerson)
	}
	if machines != 2 {
		t.Fatalf("generated %d machine bookings, want 2 (one per rule)", machines)
	}
	if companions != 1 {
		t.Fatalf("generated %d companions, want exactly 1 — the rule whose helper is gone must book the machine half alone", companions)
	}

	var (
		compID, linked, compPerson int64
		compCost                   string
	)
	if err := pool.QueryRowContext(ctx, `
		SELECT id, linked_entry_id, person_id, round(cost, 2)::text
		  FROM entries
		 WHERE neighbor_id=$1 AND billing_year_id=$2 AND unit='Mannstunde'
		   AND linked_entry_id IS NOT NULL AND idempotency_key LIKE 'recur:%'`,
		nid, yearID).Scan(&compID, &linked, &compPerson, &compCost); err != nil {
		t.Fatalf("read companion: %v", err)
	}
	if compPerson != livePID {
		t.Errorf("companion attributed to person %d, want %d", compPerson, livePID)
	}
	if compCost != "60.00" {
		t.Errorf("companion cost = %s, want 60.00 (2 h × 30,00 frozen in the template)", compCost)
	}
	// The link points at the machine booking of the SAME occurrence.
	var linkedKey, compKey string
	if err := pool.QueryRowContext(ctx,
		`SELECT idempotency_key FROM entries WHERE id=$1`, linked).Scan(&linkedKey); err != nil {
		t.Fatalf("read linked machine booking: %v", err)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT idempotency_key FROM entries WHERE id=$1`, compID).Scan(&compKey); err != nil {
		t.Fatalf("read companion key: %v", err)
	}
	if compKey != linkedKey+"-p" {
		t.Errorf("companion key %q is not derived from the occurrence's key %q", compKey, linkedKey)
	}

	// Re-running the same day must not book anything twice — on either half.
	if _, err := st.RunDueRecurring(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if m2, c2, s2 := generated(); m2 != machines || c2 != companions || s2 != solo {
		t.Fatalf("second run created rows: machines %d→%d, companions %d→%d, unlinked %d→%d",
			machines, m2, companions, c2, solo, s2)
	}

	// The rhythm moved on, and the run is recorded.
	rules, err := st.ListRecurring(ctx)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	var checked int
	for _, rule := range rules {
		if rule.NeighborID != nid {
			continue
		}
		checked++
		// next_run is a DATE, so compare the day, not the instant.
		if want := today.AddDate(0, 0, 7).Format("2006-01-02"); rule.NextRun.Format("2006-01-02") != want {
			t.Errorf("next run = %s, want %s", rule.NextRun.Format("2006-01-02"), want)
		}
		if rule.LastRunAt == nil {
			t.Errorf("rule %d ran but carries no last_run_at", rule.ID)
		}
		// "Jetzt ausführen" reuses the scheduled occurrence's key, so today is
		// already covered: booked, but nothing new.
		if entryID, booked, rerr := st.RunRecurringNow(ctx, rule.ID); rerr != nil || !booked || entryID != 0 {
			t.Errorf("run-now on a day already booked = (%d, %v, %v), want (0, true, nil)", entryID, booked, rerr)
		}
	}
	if checked != 3 {
		t.Fatalf("found %d rules for the fixture neighbor, want 3", checked)
	}
	if m3, c3, s3 := generated(); m3 != machines || c3 != companions || s3 != solo {
		t.Fatalf("run-now created rows: machines %d→%d, companions %d→%d, unlinked %d→%d",
			machines, m3, companions, c3, solo, s3)
	}

	t.Run("creation_rechecks_canceled_source_companion", func(t *testing.T) {
		// The handler already read this template when another request canceled
		// the companion. The store must reject that stale selection.
		stale := tmpl(livePID, liveName)
		partner, err := st.LinkedPartnerID(ctx, sourceA)
		if err != nil || partner == 0 {
			t.Fatalf("source companion: %d, %v", partner, err)
		}
		if err := st.SetEntryVoided(ctx, partner, true, "canceled before series creation"); err != nil {
			t.Fatalf("void source companion: %v", err)
		}
		if err := st.CreateRecurring(ctx, sourceA, nid, stale, "weekly", today); !errors.Is(err, store.ErrSourceCompanionUnavailable) {
			t.Fatalf("series with stale companion = %v, want ErrSourceCompanionUnavailable", err)
		}
	})

	// The rule list names the helper, so a series that books a person says so.
	for _, rule := range rules {
		if rule.NeighborID == nid && rule.Template.Companion != nil && rule.Template.Companion.PersonID == livePID {
			if got := rule.Template.Summary(); got != "Mähen · 2 h · mit "+liveName {
				t.Errorf("summary = %q, want it to name the helper", got)
			}
			t.Run("run_now_reports_restored_companion", func(t *testing.T) {
				if err := st.DeleteEntry(ctx, compID); err != nil {
					t.Fatalf("delete companion: %v", err)
				}
				entryID, booked, err := st.RunRecurringNow(ctx, rule.ID)
				if err != nil || !booked || entryID == 0 {
					t.Fatalf("restored companion reported as (%d, %v, %v), want a new entry ID", entryID, booked, err)
				}
				partner, err := st.LinkedPartnerID(ctx, linked)
				if err != nil || partner != entryID {
					t.Fatalf("reported entry %d is not the restored companion %d: %v", entryID, partner, err)
				}
			})
			t.Run("scheduled_run_counts_restored_companion", func(t *testing.T) {
				partner, err := st.LinkedPartnerID(ctx, linked)
				if err != nil || partner == 0 {
					t.Fatalf("find companion: %d, %v", partner, err)
				}
				if err := st.DeleteEntry(ctx, partner); err != nil {
					t.Fatalf("delete companion: %v", err)
				}
				if err := st.UpdateRecurring(ctx, rule.ID, "weekly", today); err != nil {
					t.Fatalf("make rule due: %v", err)
				}
				n, err := st.RunDueRecurring(ctx)
				if err != nil || n != 1 {
					t.Fatalf("restored companion count = %d, %v; want 1 so the caller records an audit", n, err)
				}
				if m, c, s := generated(); m != machines || c != companions || s != solo {
					t.Fatalf("restoring companion changed occurrence totals to %d/%d/%d", m, c, s)
				}
			})
		}
	}

	for _, fromCompanion := range []bool{false, true} {
		name := "delete_from_machine"
		if fromCompanion {
			name = "delete_from_companion"
		}
		t.Run(name+"_uses_series_lock_order", func(t *testing.T) {
			machineID := source("Lock order", livePID)
			partnerID, err := st.LinkedPartnerID(ctx, machineID)
			if err != nil || partnerID == 0 {
				t.Fatalf("find lock-test companion: %d, %v", partnerID, err)
			}
			lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			tx, err := pool.BeginTx(lockCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			var lockedID, backendPID int64
			if err := tx.QueryRowContext(lockCtx, `SELECT id, pg_backend_pid() FROM entries WHERE id=$1 FOR SHARE`, machineID).Scan(&lockedID, &backendPID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if fromCompanion {
					done <- st.DeleteEntryPair(lockCtx, partnerID, machineID)
				} else {
					done <- st.DeleteEntryPair(lockCtx, machineID, partnerID)
				}
			}()
			// Wait for the delete to block on our source lock. If it locks the
			// companion first, acquiring the series' second lock will fail.
			for {
				var waiting bool
				if err := pool.QueryRowContext(lockCtx, `SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, backendPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("delete finished before the lock was released: %v", err)
				case <-time.After(10 * time.Millisecond):
				case <-lockCtx.Done():
					t.Fatal(lockCtx.Err())
				}
			}
			if _, err := tx.ExecContext(lockCtx, `SET LOCAL lock_timeout = '1s'`); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(lockCtx, `SELECT id FROM entries WHERE id=$1 FOR SHARE`, partnerID).Scan(&lockedID); err != nil {
				t.Fatalf("series and pair deletion use opposite lock orders: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("delete pair: %v", err)
			}
		})
	}
}
