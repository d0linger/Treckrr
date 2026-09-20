package web

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestNeighborEquipmentRendersInMasterDataAndBooking(t *testing.T) {
	equipment := models.NeighborEquipment{ID: 4, NeighborID: 3, Name: "Zwangsmischer <1000>", Capacity: decimal.NewFromInt(1000), CapacityUnit: "l", BillingUnit: "h", DefaultRate: decimal.NewFromInt(12)}
	master := execPage(t, "neighbor_equipment", map[string]any{
		"Neighbor": models.Neighbor{ID: 3, Name: "Bio-Hof"}, "Equipment": []models.NeighborEquipment{equipment},
	})
	for _, want := range []string{"Zwangsmischer &lt;1000&gt;", `action="/neighbors/3/equipment/4/update"`, "1.000 l", "12,00"} {
		if !strings.Contains(master, want) {
			t.Errorf("master page missing %q", want)
		}
	}
	booking := execPage(t, "neighbor", map[string]any{
		"Base": models.PriceBase{ID: 1}, "Year": models.BillingYear{ID: 2, Year: 2026},
		"Neighbor": models.Neighbor{ID: 3, Name: "Bio-Hof"}, "NeighborEquipment": []models.NeighborEquipment{equipment},
		"BookingValues": map[string]string{"booking_kind": "equipment", "booking_direction": "in", "mode": "free"},
		"Saldo":         decimal.Zero, "TotalHours": decimal.Zero, "PaidSum": decimal.Zero, "Remaining": decimal.Zero,
		"Entries": []models.Entry{},
	})
	for _, want := range []string{`name="neighbor_equipment_id"`, `value="4"`, `data-equipment-rate="12"`, `href="/neighbors/3/equipment"`} {
		if !strings.Contains(booking, want) {
			t.Errorf("booking form missing %q", want)
		}
	}
}
