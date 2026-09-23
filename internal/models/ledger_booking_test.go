package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLedgerBookingSnapshot verifies independent rounding and lossless metadata.
func TestLedgerBookingSnapshot(t *testing.T) {
	t.Parallel()
	gespannID, tractorID, loadLevelID := int64(7), int64(8), int64(9)
	b := LedgerBooking{Version: 1, Kind: "equipment", TaskLabel: "Heuernte", Unit: "h",
		Quantity: dec("1.005"), UnitPrice: dec("1"), PartnerLabel: "Nachbars Gespann",
		PartnerPerson: "Franz", PersonHours: dec("2.005"), PersonRate: dec("1"), Note: "Wiese",
		Mode: "gespann", GespannID: &gespannID, TractorID: &tractorID, LoadLevelID: &loadLevelID,
		MachineIDs: []int64{10, 11}}
	if got := b.Total().StringFixed(2); got != "3.02" {
		t.Fatalf("independent line rounding: %s", got)
	}
	if got := b.ServiceCost().StringFixed(2); got != "1.01" {
		t.Fatalf("service line rounding: %s", got)
	}
	for _, want := range []string{"Heuernte", "Nachbars Gespann", "1,005 h", "Franz", "2,005 Mannstunden", "Wiese"} {
		if !strings.Contains(b.Summary(), want) {
			t.Errorf("summary omitted %q: %s", want, b.Summary())
		}
	}
	blob, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var restored LedgerBooking
	if err := json.Unmarshal(blob, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Summary() != b.Summary() || !restored.Total().Equal(b.Total()) ||
		restored.GespannID == nil || *restored.GespannID != gespannID || restored.TractorID == nil || *restored.TractorID != tractorID ||
		restored.LoadLevelID == nil || *restored.LoadLevelID != loadLevelID || len(restored.MachineIDs) != 2 || restored.MachineIDs[1] != 11 {
		t.Fatal("snapshot failed JSON round trip")
	}
	b.UnitPrice, b.PersonRate = dec("20.005"), dec("12.3456")
	if !strings.Contains(b.Summary(), "20,005 €") || !strings.Contains(b.Summary(), "12,3456 €") {
		t.Fatalf("document summary lost agreed-rate precision: %s", b.Summary())
	}
}

// TestLedgerBookingSummaryDeduplicatesDerivedLabel keeps a catalog-derived task
// from appearing twice in Belege and exports.
func TestLedgerBookingSummaryDeduplicatesDerivedLabel(t *testing.T) {
	t.Parallel()
	b := LedgerBooking{Version: 1, Kind: "equipment", TaskLabel: "Zwangsmischer", PartnerLabel: " zwangsmischer ",
		Unit: "h", Quantity: dec("2.5"), UnitPrice: dec("12")}
	if got, want := b.Summary(), "Zwangsmischer · 2,5 h × 12 €"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}

// TestLedgerEntryDisplayDescription prefers current structured presentation but
// preserves manual legacy descriptions.
func TestLedgerEntryDisplayDescription(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		row  LedgerEntry
		want string
	}{
		{
			name: "structured booking",
			row: LedgerEntry{Description: "stale duplicate", Booking: &LedgerBooking{Version: 1, Kind: "equipment",
				TaskLabel: "Zwangsmischer", PartnerLabel: "Zwangsmischer", Unit: "h", Quantity: dec("2.5"), UnitPrice: dec("12")}},
			want: "Zwangsmischer · 2,5 h × 12 €",
		},
		{name: "legacy posting", row: LedgerEntry{Description: "Gegenleistung Heuernte"}, want: "Gegenleistung Heuernte"},
		{name: "empty legacy posting", row: LedgerEntry{}, want: "Verrechnung"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.row.DisplayDescription(); got != tt.want {
				t.Fatalf("DisplayDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLedgerBookingLegacyEquipmentSnapshotCompatibility prevents typed edits
// from erasing snapshot keys written by the retired neighbor-specific catalog.
func TestLedgerBookingLegacyEquipmentSnapshotCompatibility(t *testing.T) {
	t.Parallel()
	const raw = `{"version":1,"kind":"equipment","task_label":"Altbestand","unit":"h","quantity":"2","unit_price":"12","partner_label":"Mischer · 1000 l","neighbor_equipment_id":5,"equipment_capacity":"1000","equipment_capacity_unit":"l","equipment_billing_unit":"h"}`
	var booking LedgerBooking
	if err := json.Unmarshal([]byte(raw), &booking); err != nil {
		t.Fatal(err)
	}
	if booking.NeighborEquipmentID == nil || *booking.NeighborEquipmentID != 5 || booking.EquipmentCapacityUnit != "l" {
		t.Fatalf("legacy snapshot not decoded: %+v", booking)
	}
	blob, err := json.Marshal(booking)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"neighbor_equipment_id":5`, `"equipment_capacity":"1000"`, `"equipment_billing_unit":"h"`} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("legacy snapshot key lost after round trip: %s", want)
		}
	}
}
