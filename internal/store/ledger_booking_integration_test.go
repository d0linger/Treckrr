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

// TestLedgerBookingLifecycle verifies durable metadata, invoice separation,
// account locks, strict retries, edits, reversal and anonymization on scratch data.
func TestLedgerBookingLifecycle(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	date := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	in := store.LedgerBookingInput{YearID: yearID, NeighborID: neighborID, Date: date, Incoming: true,
		IdempotencyKey: "unified-counter-1", Booking: models.LedgerBooking{Version: 1, Kind: "equipment", TaskLabel: "Heuernte",
			Unit: "h", Quantity: dec("2"), UnitPrice: dec("50"), PartnerLabel: "Nachbars Gespann",
			PartnerPerson: "Franz", PersonHours: dec("3.5"), PersonRate: dec("20"), Note: "Wiese"}}
	id, err := st.CreateLedgerBooking(ctx, in)
	if err != nil || id == 0 {
		t.Fatalf("create=%d %v", id, err)
	}
	_, _, saved, err := st.GetLedgerEntry(ctx, id)
	if err != nil || saved.Booking == nil || saved.Amount.StringFixed(2) != "-170.00" || saved.Booking.Summary() != in.Booking.Summary() {
		t.Fatalf("roundtrip=%+v error=%v", saved, err)
	}
	if replay, err := st.CreateLedgerBooking(ctx, in); err != nil || replay != 0 {
		t.Fatalf("replay=%d %v", replay, err)
	}
	changed := in
	changed.Booking.Quantity = dec("3")
	if _, err := st.CreateLedgerBooking(ctx, changed); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("changed retry=%v", err)
	}
	entry := &models.Entry{NeighborID: neighborID, BillingYearID: yearID, Date: date, Unit: "h", Hours: dec("2"), HourlyRate: dec("50"), Cost: dec("100"), IdempotencyKey: in.IdempotencyKey}
	if _, err := st.CreateEntry(ctx, entry, nil); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("ledger to entry collision=%v", err)
	}
	entry.IdempotencyKey = "outgoing-1"
	entry.RequestFingerprint = "original-active-form"
	if _, err := st.CreateEntry(ctx, entry, nil); err != nil {
		t.Fatal(err)
	}
	entry.RequestFingerprint = "changed-active-form"
	if _, err := st.CreateEntry(ctx, entry, nil); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("changed outgoing retry=%v", err)
	}
	entry.RequestFingerprint = ""
	if _, err := st.CreateEntry(ctx, entry, nil); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("stripped unified fields bypassed retry identity=%v", err)
	}
	entry.RequestFingerprint = "original-active-form"
	collision := in
	collision.IdempotencyKey = entry.IdempotencyKey
	if _, err := st.CreateLedgerBooking(ctx, collision); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("entry to ledger collision=%v", err)
	}
	if err := st.UpdateNeighborLedger(ctx, id, dec("999"), "tamper", date); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("legacy overwrite=%v", err)
	}
	changed.Booking.PersonHours = dec("1")
	wrongKind := changed
	wrongKind.Booking.Kind = "quantity"
	if err := st.UpdateLedgerBooking(ctx, id, wrongKind); !errors.Is(err, store.ErrBookingKindLocked) {
		t.Fatalf("changed kind on edit=%v", err)
	}
	if err := st.UpdateLedgerBooking(ctx, id, changed); err != nil {
		t.Fatal(err)
	}
	_, _, edited, err := st.GetLedgerEntry(ctx, id)
	if err != nil || edited.Booking == nil || edited.Booking.Quantity.String() != "3" || edited.Booking.PersonHours.String() != "1" {
		t.Fatalf("edited metadata=%+v %v", edited, err)
	}
	if sum, err := st.NeighborLedgerSum(ctx, yearID, neighborID); err != nil || sum.StringFixed(2) != "-170.00" {
		t.Fatalf("editedsum=%s %v", sum, err)
	}
	if err := st.SetLedgerVoided(ctx, id, true, "test correction"); err != nil {
		t.Fatal(err)
	}
	if sum, err := st.NeighborLedgerSum(ctx, yearID, neighborID); err != nil || !sum.IsZero() {
		t.Fatalf("voidedsum=%s %v", sum, err)
	}
	if err := st.SetLedgerVoided(ctx, id, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCompany(ctx, models.Company{Name: "Test Farm", Address: "Test Address", TaxMode: "regel", VATRate: dec("20")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE neighbors SET address='Test neighbor address' WHERE id=$1`, neighborID); err != nil {
		t.Fatal(err)
	}
	content, err := st.BuildInvoiceContent(ctx, yearID, neighborID)
	if err != nil || content.Net.StringFixed(2) != "100.00" || content.Gross.StringFixed(2) != "120.00" || len(content.Lines) != 1 {
		t.Fatalf("outgoing invoice included counterclaim: %+v %v", content, err)
	}
	if _, err := st.IssueInvoice(ctx, yearID, neighborID, 2026, date); err != nil {
		t.Fatal(err)
	}
	if remaining, err := st.AccountRemaining(ctx, yearID, neighborID); err != nil || remaining.StringFixed(2) != "-50.00" {
		t.Fatalf("settlement=%s %v", remaining, err)
	}
	locked := in
	locked.IdempotencyKey = "locked-new"
	if _, err := st.CreateLedgerBooking(ctx, locked); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("invoice lock create=%v", err)
	}
	if err := st.UpdateLedgerBooking(ctx, id, changed); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("invoice lock update=%v", err)
	}
	if err := st.SetLedgerVoided(ctx, id, true, "blocked"); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("invoice lock void=%v", err)
	}
	if err := st.DeleteNeighborLedger(ctx, id); !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("invoice lock delete=%v", err)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatal(err)
	}
	_, _, erased, err := st.GetLedgerEntry(ctx, id)
	if err != nil || erased.Booking == nil || erased.Booking.PartnerPerson != "" || erased.Booking.PartnerLabel != "" || erased.Booking.TaskLabel != "" || erased.Booking.Note != "" || erased.Amount.StringFixed(2) != "-170.00" {
		t.Fatalf("metadata erasure=%+v %v", erased, err)
	}
}

// TestLedgerBookingConcurrentReplay proves concurrent retries create one position
// and that a completed account cannot accept a new counterclaim.
func TestLedgerBookingConcurrentReplay(t *testing.T) {
	st, _, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	in := store.LedgerBookingInput{YearID: yearID, NeighborID: neighborID, Date: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), Incoming: true,
		IdempotencyKey: "concurrent-counter", Booking: models.LedgerBooking{Version: 1, Kind: "fixed", TaskLabel: "Kosten", Unit: "Pauschale", Quantity: dec("1"), UnitPrice: dec("25")}}
	var group sync.WaitGroup
	errorsCh := make(chan error, 12)
	for range 12 {
		group.Go(func() { _, err := st.CreateLedgerBooking(ctx, in); errorsCh <- err })
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListNeighborLedger(ctx, yearID, neighborID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("concurrent rows=%d %v", len(rows), err)
	}
	wrongDirection := in
	wrongDirection.Incoming = false
	if err := st.UpdateLedgerBooking(ctx, rows[0].ID, wrongDirection); !errors.Is(err, store.ErrBookingKindLocked) {
		t.Fatalf("changed direction on edit=%v", err)
	}
	if err := st.SetYearStatus(ctx, yearID, "completed"); err != nil {
		t.Fatal(err)
	}
	if id, err := st.CreateLedgerBooking(ctx, in); err != nil || id != 0 {
		t.Fatalf("closed account no-op=%d %v", id, err)
	}
	in.IdempotencyKey = "new-after-close"
	if _, err := st.CreateLedgerBooking(ctx, in); !errors.Is(err, store.ErrYearCompleted) {
		t.Fatalf("closedyear=%v", err)
	}
}
