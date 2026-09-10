package web

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestClosedYearBelegHasNoCorrectionForms(t *testing.T) {
	for _, issued := range []bool{false, true} {
		data := map[string]any{
			"Neighbor": models.Neighbor{ID: 2, Name: "Test"},
			"Year":     models.BillingYear{ID: 1, Year: 2026},
			"Company":  models.Company{}, "HasInvoice": issued,
			"Invoice":   models.Invoice{Number: "2026-001", IssuedOn: time.Now()},
			"InvIssuer": models.InvoiceParty{}, "InvRecipient": models.InvoiceParty{},
			"TotalCost": decimal.Zero, "TotalHours": decimal.Zero,
			"Saldo": decimal.Zero, "LedgerSum": decimal.Zero, "InvRest": decimal.Zero,
			"InvBrutto": decimal.Zero, "AnzahlungSum": decimal.Zero,
			"Anzahlungen": []models.Invoice{{ID: 3, Status: "issued", Number: "2026-A001"}},
		}
		// First prove the fixture exercises the live mutation controls.
		open := execPage(t, "beleg", data)
		if !strings.Contains(open, `action="/documents/3/storno"`) || !strings.Contains(open, "/gutschrift\"") {
			t.Fatal("open-year fixture does not render correction controls")
		}
		data["Completed"] = true
		closed := execPage(t, "beleg", data)
		for _, action := range []string{"action=\"/documents/3/storno\"", "/anzahlung\"", "/gutschrift\"", "/invoice/storno\""} {
			if strings.Contains(closed, action) {
				t.Errorf("closed-year beleg still renders %s", action)
			}
		}
	}
}

func TestNeighborEmptyStateOnlyOnce(t *testing.T) {
	for _, scope := range []string{"active", "archived", "all"} {
		html := execPage(t, "neighbors_manage", map[string]any{"Scope": scope})
		if n := strings.Count(html, "empty__title"); n != 1 {
			t.Errorf("scope %s renders %d empty states", scope, n)
		}
	}
}
