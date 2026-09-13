package web

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestNeighborBookingSubmitGuards keeps both creation controls consistent with
// invoice finalization and hides them entirely once the billing year is closed.
func TestNeighborBookingSubmitGuards(t *testing.T) {
	for _, tc := range []struct {
		name              string
		issued, completed bool
	}{
		{name: "open"},
		{name: "invoiced", issued: true},
		{name: "closed", completed: true},
		{name: "closed invoiced", issued: true, completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := execPage(t, "neighbor", map[string]any{
				"Base": models.PriceBase{ID: 1}, "Year": models.BillingYear{ID: 1, Year: 2026},
				"Neighbor": models.Neighbor{ID: 1, Name: "Testhof"},
				"Gespanne": []models.Gespann{{ID: 1, Name: "Testgespann"}},
				"Entries":  []models.Entry{}, "Saldo": decimal.Zero, "TotalHours": decimal.Zero,
				"PaidSum": decimal.Zero, "Remaining": decimal.Zero,
				"HasInvoice": tc.issued, "Completed": tc.completed,
			})
			for _, label := range []string{"Buchung speichern", "Zeilen speichern"} {
				button := regexp.MustCompile(`<button\b[^>]*>` + label + `</button>`).FindString(page)
				if tc.completed {
					if button != "" {
						t.Errorf("closed year exposes %q", label)
					}
					continue
				}
				if button == "" {
					t.Fatalf("missing button %q", label)
				}
				if disabled := strings.Contains(button, " disabled"); disabled != tc.issued {
					t.Errorf("%q disabled=%t, want %t", label, disabled, tc.issued)
				}
			}
		})
	}
}

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
