//go:build integration

package store_test

import (
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestInvoiceClaimsFreezeOnlyTheirSources exercises every individual writer and
// proves both the source row and the issued invoice remain byte-for-byte stable.
func TestInvoiceClaimsFreezeOnlyTheirSources(t *testing.T) {
	st, pool, yearID, neighborID, frozenID := invoiceFixture(t, false)
	ctx := t.Context()
	photoID, err := st.AddEntryPhoto(ctx, frozenID, []byte("synthetic receipt"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	iv := issueFixtureInvoice(t, st, yearID, neighborID)
	var invoiceBefore, sourceBefore string
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, iv.ID).Scan(&invoiceBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM entries e WHERE id=$1`, frozenID).Scan(&sourceBefore); err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetEntry(ctx, frozenID)
	if err != nil || !frozen.InvoiceLocked {
		t.Fatalf("claimed entry = %+v, %v", frozen, err)
	}
	edited := *frozen
	edited.Cost, edited.UnitPrice = dec("999"), dec("999")
	checks := []struct {
		name string
		run  func() error
	}{
		{"update", func() error { return st.UpdateEntry(ctx, &edited, nil) }},
		{"update with partner", func() error {
			_, err := st.UpdateEntryWithPartner(ctx, &edited, nil, true)
			return err
		}},
		{"update group", func() error { return st.UpdateEntryGroup(ctx, &edited, nil, nil) }},
		{"void", func() error { return st.SetEntryVoided(ctx, frozenID, true, "forbidden") }},
		{"delete", func() error { return st.DeleteEntry(ctx, frozenID) }},
		{"sync hours", func() error {
			_, err := st.SyncPairHours(ctx, frozenID, dec("2"))
			return err
		}},
		{"add receipt", func() error {
			_, err := st.AddEntryPhoto(ctx, frozenID, []byte("new synthetic receipt"), "image/jpeg")
			return err
		}},
		{"delete receipt", func() error { return st.DeleteEntryPhoto(ctx, frozenID, photoID) }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, store.ErrInvoiceLocked) {
				t.Fatalf("error = %v, want ErrInvoiceLocked", err)
			}
		})
	}
	pendingID, err := st.CreateEntry(ctx, flatEntry(yearID, neighborID, 2026, "50"), nil)
	if err != nil || pendingID == 0 {
		t.Fatalf("new work = %d, %v", pendingID, err)
	}
	pending, err := st.GetEntry(ctx, pendingID)
	if err != nil || pending.InvoiceLocked {
		t.Fatalf("pending entry = %+v, %v", pending, err)
	}
	pending.Cost, pending.UnitPrice = dec("60"), dec("60")
	if err := st.UpdateEntry(ctx, pending, nil); err != nil {
		t.Fatal(err)
	}
	overview, _, _, err := st.FilterBookings(ctx, store.BookingFilter{EntryFilter: store.EntryFilter{YearID: yearID}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, row := range overview {
		seen[row.ID] = row.InvoiceLocked
	}
	if !seen[frozenID] || seen[pendingID] {
		t.Fatalf("overview claim flags = %+v", seen)
	}
	pendingPhoto, err := st.AddEntryPhoto(ctx, pendingID, []byte("pending receipt"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteEntryPhoto(ctx, pendingID, pendingPhoto); err != nil {
		t.Fatal(err)
	}
	for _, voided := range []bool{true, false} {
		if err := st.SetEntryVoided(ctx, pendingID, voided, "correction"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteEntryPair(ctx, pendingID, frozenID); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("mixed frozen pair deletion = %v", err)
	}
	if _, err := st.GetEntry(ctx, pendingID); err != nil {
		t.Fatalf("failed pair deletion did not roll back: %v", err)
	}
	count, err := st.VoidBookings(ctx, []store.BookingRef{{Source: "entry", ID: frozenID}, {Source: "entry", ID: pendingID}}, true, "bulk")
	if err != nil || count != 1 {
		t.Fatalf("bulk void = %d, %v, want 1", count, err)
	}
	count, err = st.DeleteEntries(ctx, []int64{frozenID, pendingID})
	if err != nil || count != 1 {
		t.Fatalf("bulk delete = %d, %v, want 1", count, err)
	}
	var invoiceAfter, sourceAfter string
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, iv.ID).Scan(&invoiceAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM entries e WHERE id=$1`, frozenID).Scan(&sourceAfter); err != nil {
		t.Fatal(err)
	}
	if invoiceBefore != invoiceAfter || sourceBefore != sourceAfter {
		t.Fatal("a frozen invoice or source changed")
	}
	if _, err := st.StornoInvoice(ctx, yearID, neighborID, "source release"); err != nil {
		t.Fatal(err)
	}
	released, err := st.GetEntry(ctx, frozenID)
	if err != nil || released.InvoiceLocked {
		t.Fatalf("released entry = %+v, %v", released, err)
	}
	if err := st.DeleteEntryPhoto(ctx, frozenID, photoID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateEntry(ctx, &edited, nil); err != nil {
		t.Fatalf("released source remains locked: %v", err)
	}
}

// TestInvoiceClaimedHelperProtectsUnclaimedParent verifies ON DELETE SET NULL
// cannot mutate a frozen helper merely because its parent is not invoiced.
func TestInvoiceClaimedHelperProtectsUnclaimedParent(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := t.Context()
	main, people := integrationEntryGroup(yearID, neighborID, "partly-voided-group")
	mainID, ids, err := st.CreateEntryGroup(ctx, main, nil, people)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntryVoided(ctx, mainID, true, "not billable"); err != nil {
		t.Fatal(err)
	}
	issueFixtureInvoice(t, st, yearID, neighborID)
	if err := st.SetEntryVoided(ctx, mainID, false, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := st.GetEntry(ctx, mainID)
	if err != nil || pending.InvoiceLocked {
		t.Fatalf("parent = %+v, %v", pending, err)
	}
	changed := *pending
	changed.Hours, changed.Quantity, changed.Cost = dec("3"), dec("3"), dec("120")
	if _, err := st.UpdateEntryWithPartner(ctx, &changed, nil, true); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("sync into frozen helper = %v", err)
	}
	stillPending, err := st.GetEntry(ctx, mainID)
	if err != nil || !stillPending.Cost.Equal(pending.Cost) || !stillPending.Hours.Equal(pending.Hours) {
		t.Fatalf("failed partner sync did not roll back parent: %+v, %v", stillPending, err)
	}
	if err := st.UpdateEntryGroup(ctx, pending, nil, nil); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("omitting a frozen helper = %v", err)
	}
	if err := st.DeleteEntry(ctx, mainID); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("deleting parent = %v", err)
	}
	if count, err := st.DeleteEntries(ctx, []int64{mainID}); err != nil || count != 0 {
		t.Fatalf("bulk parent deletion = %d, %v", count, err)
	}
	helper, err := st.GetEntry(ctx, ids[0])
	if err != nil || helper.LinkedEntryID == nil || *helper.LinkedEntryID != mainID || !helper.InvoiceLocked {
		t.Fatalf("frozen helper was unlinked: %+v, %v", helper, err)
	}
}

// TestPendingRecalcAfterInvoice leaves the old source, amount and pricing stamp
// unchanged while later work on that same account can adopt updated rates.
func TestPendingRecalcAfterInvoice(t *testing.T) {
	st, pool, yearID, neighborID, frozenID := invoiceFixture(t, false)
	ctx := t.Context()
	year, err := st.GetBillingYear(ctx, yearID)
	if err != nil {
		t.Fatal(err)
	}
	machineID, err := st.CreateMachine(ctx, year.BaseID, "Claim test machine", dec("1"), dec("100"), "", 0, dec("0"))
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := st.GetEntry(ctx, frozenID)
	if err != nil {
		t.Fatal(err)
	}
	frozen.Unit, frozen.Hours, frozen.Quantity = "h", dec("1"), dec("1")
	frozen.HourlyRate, frozen.UnitPrice = dec("100"), dec("100")
	if err := st.UpdateEntry(ctx, frozen, []int64{machineID}); err != nil {
		t.Fatal(err)
	}
	issueFixtureInvoice(t, st, yearID, neighborID)
	var before string
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM entries e WHERE id=$1`, frozenID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	pending := *frozen
	pending.ID = 0
	pending.TaskLabel = "Later work"
	pendingID, err := st.CreateEntry(ctx, &pending, []int64{machineID})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateMachine(ctx, year.BaseID, machineID, "Claim test machine", dec("1"), dec("200"), "", 0, dec("0")); err != nil {
		t.Fatal(err)
	}
	rows, err := st.RecalcPreview(ctx, yearID, &neighborID)
	if err != nil || len(rows) != 1 || rows[0].EntryID != pendingID {
		t.Fatalf("pending-only preview = %+v, %v", rows, err)
	}
	count, oldTotal, newTotal, err := st.ApplyRecalc(ctx, yearID, &neighborID)
	if err != nil || count != 1 || !oldTotal.Equal(dec("100")) || !newTotal.Equal(dec("200")) {
		t.Fatalf("recalc = %d, %s, %s, %v", count, oldTotal, newTotal, err)
	}
	var after string
	if err := pool.QueryRowContext(ctx, `SELECT to_jsonb(e)::text FROM entries e WHERE id=$1`, frozenID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("recalc changed frozen source or priced_at")
	}
	if err := st.SetYearStatus(ctx, yearID, models.YearCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEntry(ctx, flatEntry(yearID, neighborID, 2026, "25"), nil); !errors.Is(err, store.ErrYearCompleted) {
		t.Fatalf("closed year accepted work: %v", err)
	}
	if _, err := st.AddEntryPhoto(ctx, pendingID, []byte("closed-year receipt"), "image/jpeg"); !errors.Is(err, store.ErrYearCompleted) {
		t.Fatalf("closed year accepted receipt: %v", err)
	}
}

// TestEntryLinksStayInsideMutableAccount rejects new cross-account links and
// refuses to detach legacy foreign links under an incomplete lock boundary.
func TestEntryLinksStayInsideMutableAccount(t *testing.T) {
	st, pool, yearID, neighborID, frozenID := invoiceFixture(t, false)
	ctx := t.Context()
	issueFixtureInvoice(t, st, yearID, neighborID)
	helper := flatEntry(yearID, neighborID, 2026, "25")
	helper.LinkedEntryID = &frozenID
	if _, err := st.CreateEntry(ctx, helper, nil); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("extension of frozen group = %v", err)
	}
	other := addYearNeighbor(t, st, pool, yearID, "Foreign link fixture")
	helper.NeighborID = other
	if _, err := st.CreateEntry(ctx, helper, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-account parent accepted = %v", err)
	}
	pendingID, err := st.CreateEntry(ctx, flatEntry(yearID, neighborID, 2026, "30"), nil)
	if err != nil {
		t.Fatal(err)
	}
	helper.LinkedEntryID = nil
	foreignID, err := st.CreateEntry(ctx, helper, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce an invalid historical link only inside the disposable fixture.
	if _, err := pool.ExecContext(ctx, `UPDATE entries SET linked_entry_id=$1 WHERE id=$2`, pendingID, foreignID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteEntry(ctx, pendingID); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("foreign incoming link deletion = %v", err)
	}
	if count, err := st.DeleteEntries(ctx, []int64{pendingID}); err != nil || count != 0 {
		t.Fatalf("foreign incoming link bulk deletion = %d, %v", count, err)
	}
	saved, err := st.GetEntry(ctx, foreignID)
	if err != nil || saved.LinkedEntryID == nil || *saved.LinkedEntryID != pendingID {
		t.Fatalf("legacy foreign link changed = %+v, %v", saved, err)
	}
}
