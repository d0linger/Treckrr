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
