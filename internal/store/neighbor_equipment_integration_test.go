//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestNeighborEquipmentLifecycleIntegration proves ownership scoping, archived
// filtering, immutable ledger snapshots, delete protection and anonymization.
func TestNeighborEquipmentLifecycleIntegration(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	equipment := models.NeighborEquipment{
		NeighborID: neighborID, Name: "Zwangsmischer", Capacity: dec("1000"),
		CapacityUnit: "l", BillingUnit: "h", DefaultRate: dec("12"), Note: "ohne Traktor",
	}
	id, err := st.CreateNeighborEquipment(ctx, equipment)
	if err != nil {
		t.Fatal(err)
	}
	equipment.ID = id
	got, err := st.GetNeighborEquipment(ctx, id)
	if err != nil || got.Name != equipment.Name || got.Capacity.String() != "1000" || got.BillingUnit != "h" {
		t.Fatalf("created equipment = %+v, %v", got, err)
	}
	active, err := st.ActiveNeighborEquipment(ctx, neighborID)
	if err != nil || len(active) != 1 || active[0].ID != id {
		t.Fatalf("active equipment = %+v, %v", active, err)
	}
	if err := st.SetNeighborEquipmentArchived(ctx, id, neighborID, true); err != nil {
		t.Fatal(err)
	}
	active, err = st.ActiveNeighborEquipment(ctx, neighborID)
	if err != nil || len(active) != 0 {
		t.Fatalf("archived equipment offered for booking = %+v, %v", active, err)
	}
	if err := st.SetNeighborEquipmentArchived(ctx, id, neighborID, false); err != nil {
		t.Fatal(err)
	}

	personID, err := st.CreatePerson(ctx, fmt.Sprintf("Fremdhelfer %d", neighborID), dec("19"), "")
	if err != nil {
		t.Fatal(err)
	}
	booking := models.LedgerBooking{
		Version: 1, Kind: "equipment", TaskLabel: "Beton mischen", Unit: "h",
		Quantity: dec("2.5"), UnitPrice: dec("12"), PartnerLabel: "Zwangsmischer · 1000 l",
		NeighborEquipmentID: &id, EquipmentCapacity: dec("1000"), EquipmentCapacityUnit: "l", EquipmentBillingUnit: "h",
		People: []models.BookingPerson{{ID: 1, PersonID: &personID, Name: "Fremdhelfer", Hours: dec("2.5"), Rate: dec("19")}},
	}
	ledgerID, err := st.CreateLedgerBooking(ctx, store.LedgerBookingInput{
		YearID: yearID, NeighborID: neighborID, Date: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
		Incoming: true, Booking: booking,
	})
	if err != nil {
		t.Fatal(err)
	}
	equipment.Name, equipment.DefaultRate = "Zwangsmischer neu", dec("99")
	if err := st.UpdateNeighborEquipment(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	_, _, stored, err := st.GetLedgerEntry(ctx, ledgerID)
	if err != nil || stored.Booking == nil || stored.Booking.PartnerLabel != "Zwangsmischer · 1000 l" || stored.Booking.UnitPrice.String() != "12" {
		t.Fatalf("booking snapshot changed with master data: %+v, %v", stored, err)
	}
	if err := st.DeleteNeighborEquipment(ctx, id, neighborID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("delete used equipment = %v, want ErrHasHistory", err)
	}
	if err := st.DeletePerson(ctx, personID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("delete person used by incoming booking = %v, want ErrHasHistory", err)
	}

	otherID, err := st.CreateNeighbor(ctx, "Other neighbor", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNeighborEquipment(ctx, models.NeighborEquipment{ID: id, NeighborID: otherID, Name: "Cross account", BillingUnit: "h"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-neighbor update = %v, want ErrNotFound", err)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatal(err)
	}
	_, _, anonymized, err := st.GetLedgerEntry(ctx, ledgerID)
	if err != nil {
		t.Fatal(err)
	}
	if anonymized.Booking == nil || len(anonymized.Booking.People) != 1 ||
		anonymized.Booking.People[0].Name != "" || anonymized.Booking.People[0].PersonID != nil {
		t.Fatalf("ledger person identity survived anonymization: %+v", anonymized.Booking)
	}
	var count int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM neighbor_equipment WHERE neighbor_id=$1`, neighborID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("equipment survived anonymization: count=%d err=%v", count, err)
	}
	if _, err := st.CreateNeighborEquipment(ctx, equipment); !errors.Is(err, store.ErrNeighborAnonymized) {
		t.Fatalf("equipment recreated after anonymization = %v, want ErrNeighborAnonymized", err)
	}
}
