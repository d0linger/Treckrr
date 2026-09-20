//go:build integration

package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestCreateEntryGroupIntegration exercises multiple people on every entry
// basis, exact monetary snapshots, deterministic retries and copy independence.
func TestCreateEntryGroupIntegration(t *testing.T) {
	for _, unit := range []string{"h", "Ballen", models.UnitMannstunde} {
		t.Run(unit, func(t *testing.T) {
			st, pool, yearID, neighborID := scratchBookingFixture(t)
			ctx := t.Context()
			personID, err := st.CreatePerson(ctx, "Maintained person", dec("30"), "")
			if err != nil {
				t.Fatal(err)
			}
			main, people := integrationEntryGroup(yearID, neighborID, "group-original")
			main.Unit = unit
			if unit == models.UnitMannstunde {
				main.PersonName = "Primary free-text person"
			}
			people[0].PersonID, people[0].PersonName = &personID, ""
			mainID, ids, err := st.CreateEntryGroup(ctx, main, nil, people)
			if err != nil || mainID == 0 || len(ids) != 2 || ids[0] == 0 || ids[1] == 0 || ids[0] == ids[1] {
				t.Fatalf("create = %d, %v, %v", mainID, ids, err)
			}
			saved, err := st.EntryCompanions(ctx, mainID)
			if err != nil || len(saved) != 2 {
				t.Fatalf("companions = %+v, %v", saved, err)
			}
			if saved[0].PersonID == nil || *saved[0].PersonID != personID || saved[0].PersonName != "Maintained person" ||
				saved[1].PersonID != nil || saved[1].PersonName != "Free-text helper" || saved[1].Cost.StringFixed(2) != "24.69" {
				t.Fatalf("snapshots = %+v", saved)
			}
			if err := st.UpdatePerson(ctx, personID, "Renamed person", dec("90"), ""); err != nil {
				t.Fatal(err)
			}
			if err := st.SetPersonArchived(ctx, personID, true); err != nil {
				t.Fatal(err)
			}
			if id, replayIDs, err := st.CreateEntryGroup(ctx, main, nil, people); err != nil || id != 0 || len(replayIDs) != 2 || replayIDs[0] != 0 || replayIDs[1] != 0 {
				t.Fatalf("replay = %d, %v, %v", id, replayIDs, err)
			}
			stored, err := st.GetEntry(ctx, ids[0])
			if err != nil || stored.PersonName != "Maintained person" || stored.UnitPrice.String() != "30" {
				t.Fatalf("master-data change repriced snapshot: %+v, %v", stored, err)
			}
			copyMain := *main
			copyMain.IdempotencyKey = "group-copy"
			copyPeople := make([]*models.Entry, len(saved))
			for i := range saved {
				copyPerson := saved[i]
				copyPerson.ID, copyPerson.LinkedEntryID = 0, nil
				copyPerson.IdempotencyKey, copyPerson.RequestFingerprint = "", ""
				copyPeople[i] = &copyPerson
			}
			copyID, copyIDs, err := st.CreateEntryGroup(ctx, &copyMain, nil, copyPeople)
			if err != nil || copyID == mainID || len(copyIDs) != 2 || copyIDs[0] == ids[0] || copyIDs[1] == ids[1] {
				t.Fatalf("copy = %d, %v, %v", copyID, copyIDs, err)
			}
			var count int
			if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action='entry_group_add'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("creation audit count = %d, %v", count, err)
			}
		})
	}
}

// TestCreateEntryGroupReplayRecovery verifies strict identity, orphan recovery,
// rollback on a missing reference, and isolation from independent helper rows.
func TestCreateEntryGroupReplayRecovery(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	main, people := integrationEntryGroup(yearID, neighborID, "recovery")
	mainID, ids, err := st.CreateEntryGroup(ctx, main, nil, people)
	if err != nil {
		t.Fatal(err)
	}
	changed := *people[0]
	changed.Quantity = dec("9")
	if _, _, err := st.CreateEntryGroup(ctx, main, nil, []*models.Entry{&changed, people[1]}); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	if err := st.DeleteEntry(ctx, mainID); err != nil {
		t.Fatal(err)
	}
	newID, restored, err := st.CreateEntryGroup(ctx, main, nil, people)
	if err != nil || newID == 0 || restored[0] != 0 || restored[1] != 0 {
		t.Fatalf("restore primary = %d, %v, %v", newID, restored, err)
	}
	saved, err := st.EntryCompanions(ctx, newID)
	if err != nil || len(saved) != 2 || saved[0].ID != ids[0] || saved[1].ID != ids[1] {
		t.Fatalf("relinked people = %+v, %v", saved, err)
	}
	if err := st.DeleteEntry(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if id, repaired, err := st.CreateEntryGroup(ctx, main, nil, people); err != nil || id != 0 || repaired[0] != 0 || repaired[1] == 0 {
		t.Fatalf("restore one helper = %d, %v, %v", id, repaired, err)
	}
	badMain, badPeople := integrationEntryGroup(yearID, neighborID, "rollback")
	missingPersonID := int64(999999)
	badPeople[1].PersonID = &missingPersonID
	if _, _, err := st.CreateEntryGroup(ctx, badMain, nil, badPeople); err == nil {
		t.Fatal("missing person must fail the whole group")
	}
	var count int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE idempotency_key='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left main row: %d, %v", count, err)
	}
	var auditCount int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action='entry_group_add'`).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("failed/replayed writes emitted audit: %d, %v", auditCount, err)
	}
	// Reusing a key belonging to a different group cannot steal its helper,
	// even if every account and fingerprint value is deliberately matched.
	otherMain, otherPeople := integrationEntryGroup(yearID, neighborID, "unrelated")
	otherMain.RequestFingerprint = saved[0].RequestFingerprint
	var storedKey string
	if err := pool.QueryRowContext(ctx, `SELECT idempotency_key FROM entries WHERE id=$1`, ids[0]).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	otherPeople[0].IdempotencyKey = storedKey
	if _, _, err := st.CreateEntryGroup(ctx, otherMain, nil, otherPeople); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("helper key hijack = %v", err)
	}
}

// TestUpdateEntryGroupIntegration verifies atomic multi-person edits, stable
// helper IDs, independent hours, removal/restoration, and immutable ownership.
func TestUpdateEntryGroupIntegration(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	main, people := integrationEntryGroup(yearID, neighborID, "edit-group")
	mainID, ids, err := st.CreateEntryGroup(ctx, main, nil, people)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := st.GetEntry(ctx, mainID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.GetEntry(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	first.Quantity, first.UnitPrice = dec("4"), dec("35")
	newPerson := *people[1]
	newPerson.PersonName, newPerson.Quantity = "Third helper", dec("3")
	edited.TaskLabel, edited.Cost = "Changed task", dec("110")
	if err := st.UpdateEntryGroup(ctx, edited, nil, []*models.Entry{first, &newPerson}); err != nil {
		t.Fatal(err)
	}
	saved, err := st.EntryCompanions(ctx, mainID)
	if err != nil || len(saved) != 3 || saved[0].ID != ids[0] || saved[0].Cost.String() != "140" ||
		!saved[1].Voided || saved[1].VoidReason == "" || saved[2].Voided || saved[2].PersonName != "Third helper" {
		t.Fatalf("edited people = %+v, %v", saved, err)
	}
	// Restore the retained helper and explicitly void the third without changing
	// the primary entry's own void state.
	if err := st.SetEntryVoided(ctx, mainID, true, "Primary void stays"); err != nil {
		t.Fatal(err)
	}
	saved[1].Voided, saved[1].VoidReason = false, ""
	saved[2].Voided, saved[2].VoidReason = true, "Helper correction"
	if err := st.UpdateEntryGroup(ctx, edited, nil, []*models.Entry{&saved[0], &saved[1], &saved[2]}); err != nil {
		t.Fatal(err)
	}
	primary, err := st.GetEntry(ctx, mainID)
	if err != nil || !primary.Voided || primary.VoidReason != "Primary void stays" {
		t.Fatalf("primary void overwritten = %+v, %v", primary, err)
	}
	// A failed helper update rolls back the preceding primary/first-helper writes.
	bad := saved[1]
	bad.PersonID = new(int64(999999))
	edited.TaskLabel = "Must roll back"
	if err := st.UpdateEntryGroup(ctx, edited, nil, []*models.Entry{&saved[0], &bad}); err == nil {
		t.Fatal("missing person must roll back update")
	}
	primary, err = st.GetEntry(ctx, mainID)
	if err != nil || primary.TaskLabel != "Changed task" {
		t.Fatalf("failed edit leaked primary mutation = %+v, %v", primary, err)
	}
	independent := *people[0]
	id, err := st.CreateEntry(ctx, &independent, nil)
	if err != nil {
		t.Fatal(err)
	}
	independent.ID = id
	if err := st.UpdateEntryGroup(ctx, edited, nil, []*models.Entry{&independent}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("independent helper retarget = %v", err)
	}
	var linked *int64
	if err := pool.QueryRowContext(ctx, `SELECT linked_entry_id FROM entries WHERE id=$1`, id).Scan(&linked); err != nil || linked != nil {
		t.Fatalf("independent entry was relinked = %v, %v", linked, err)
	}
	otherID, err := st.CreateNeighbor(ctx, "Other account", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, otherID); err != nil {
		t.Fatal(err)
	}
	forged := *edited
	forged.NeighborID = otherID
	if err := st.UpdateEntryGroup(ctx, &forged, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("forged primary account = %v", err)
	}
}

// TestEntryGroupAccountGuards proves invoice/year/privacy locks cover the entire
// group, while a complete original offline retry remains a safe no-op.
func TestEntryGroupAccountGuards(t *testing.T) {
	for _, lock := range []string{"invoice", "completed", "anonymized"} {
		t.Run(lock, func(t *testing.T) {
			st, _, yearID, neighborID, _ := invoiceFixture(t, false)
			ctx := t.Context()
			main, people := integrationEntryGroup(yearID, neighborID, "guarded")
			id, _, err := st.CreateEntryGroup(ctx, main, nil, people)
			if err != nil {
				t.Fatal(err)
			}
			want := store.ErrInvoiceLocked
			switch lock {
			case "invoice":
				issueFixtureInvoice(t, st, yearID, neighborID)
			case "completed":
				want = store.ErrYearCompleted
				if err := st.SetYearStatus(ctx, yearID, "completed"); err != nil {
					t.Fatal(err)
				}
			case "anonymized":
				want = store.ErrNeighborAnonymized
				if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
					t.Fatal(err)
				}
			}
			if lock != "anonymized" {
				if replayID, _, err := st.CreateEntryGroup(ctx, main, nil, people); err != nil || replayID != 0 {
					t.Fatalf("complete replay behind %s = %d, %v", lock, replayID, err)
				}
			}
			fresh := *main
			fresh.IdempotencyKey = "new-blocked"
			if _, _, err := st.CreateEntryGroup(ctx, &fresh, nil, people); !errors.Is(err, want) {
				t.Fatalf("create behind %s = %v, want %v", lock, err, want)
			}
			edit := *main
			edit.ID = id
			if err := st.UpdateEntryGroup(ctx, &edit, nil, nil); !errors.Is(err, want) {
				t.Fatalf("update behind %s = %v, want %v", lock, err, want)
			}
		})
	}
}

// TestCreateEntryGroupConcurrentReplay proves concurrent offline submissions
// produce one main row and exactly one copy of every person, with one audit.
func TestCreateEntryGroupConcurrentReplay(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	const workers = 6
	var wg sync.WaitGroup
	errorsCh := make(chan error, workers)
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			main, people := integrationEntryGroup(yearID, neighborID, "concurrent-group")
			_, _, err := st.CreateEntryGroup(t.Context(), main, nil, people)
			errorsCh <- err
		})
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var entries, audit int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM entries`).Scan(&entries); err != nil || entries != 3 {
		t.Fatalf("concurrent entries = %d, %v", entries, err)
	}
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action='entry_group_add'`).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("concurrent audits = %d, %v", audit, err)
	}
}

// integrationEntryGroup uses independent quantities and a four-place rate to
// expose accidental synchronized hours or intermediate rounding.
func integrationEntryGroup(yearID, neighborID int64, key string) (*models.Entry, []*models.Entry) {
	date := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	main := &models.Entry{NeighborID: neighborID, BillingYearID: yearID, Date: date,
		TaskLabel: "Group work", Unit: "h", Hours: dec("2"), HourlyRate: dec("40"),
		Quantity: dec("2"), UnitPrice: dec("40"), Cost: dec("80"), IdempotencyKey: key}
	people := []*models.Entry{
		{NeighborID: neighborID, BillingYearID: yearID, Date: date, Unit: models.UnitMannstunde,
			TaskLabel: "First helper", PersonName: "First helper", Quantity: dec("2"), UnitPrice: dec("30"), Cost: dec("60")},
		{NeighborID: neighborID, BillingYearID: yearID, Date: date, Unit: models.UnitMannstunde,
			TaskLabel: "Free-text helper", PersonName: "Free-text helper", Quantity: dec("1.2345"), UnitPrice: dec("20"), Cost: dec("24.69")},
	}
	return main, people
}
