package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLedgerBookingSnapshot verifies independent rounding and lossless metadata.
func TestLedgerBookingSnapshot(t *testing.T) {
	t.Parallel()
	b := LedgerBooking{Version: 1, Kind: "equipment", TaskLabel: "Heuernte", Unit: "h",
		Quantity: dec("1.005"), UnitPrice: dec("1"), PartnerLabel: "Nachbars Gespann",
		PartnerPerson: "Franz", PersonHours: dec("2.005"), PersonRate: dec("1"), Note: "Wiese"}
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
	if restored.Summary() != b.Summary() || !restored.Total().Equal(b.Total()) {
		t.Fatal("snapshot failed JSON round trip")
	}
	b.UnitPrice, b.PersonRate = dec("20.005"), dec("12.3456")
	if !strings.Contains(b.Summary(), "20,005 €") || !strings.Contains(b.Summary(), "12,3456 €") {
		t.Fatalf("document summary lost agreed-rate precision: %s", b.Summary())
	}
}
