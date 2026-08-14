package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestDsgvoEntryFromIncludesVoided(t *testing.T) {
	e := models.Entry{
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), TaskLabel: "Mähen",
		TractorLabel: "Fendt", Unit: "h", Quantity: decimal.RequireFromString("2.5"),
		UnitPrice: decimal.RequireFromString("40"), Cost: decimal.RequireFromString("100"),
		Voided: true, VoidReason: "Doppelbuchung",
	}
	got := dsgvoEntryFrom(e)
	if !got.Voided || got.VoidReason != "Doppelbuchung" {
		t.Errorf("voided entry not carried through: %+v", got)
	}
	if got.Task != "Mähen" || !got.Cost.Equal(decimal.RequireFromString("100")) {
		t.Errorf("entry mapping wrong: %+v", got)
	}
}

func TestDsgvoExportJSONShape(t *testing.T) {
	out := dsgvoExport{
		ExportedAt: time.Now(),
		Notice:     "test",
		Subject:    dsgvoSubjectFromNeighbor(&models.Neighbor{ID: 7, Name: "Hof Berg", Address: "Weg 1", TaxID: "ATU123"}),
		BillingYears: []dsgvoYear{{
			Year:     2026,
			Entries:  []dsgvoEntry{dsgvoEntryFrom(models.Entry{TaskLabel: "Pflügen", Unit: "ha", Cost: decimal.RequireFromString("50")})},
			Invoices: []dsgvoInvoice{dsgvoInvoiceFrom(models.Invoice{Number: "2026-007", Kind: "invoice", Status: "issued"})},
		}},
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, want := range []string{`"subject"`, `"id":7`, `"tax_id":"ATU123"`, `"billing_years"`, `"year":2026`, `"number":"2026-007"`, `"task":"Pflügen"`} {
		if !strings.Contains(js, want) {
			t.Errorf("export JSON missing %s\n%s", want, js)
		}
	}
	// An empty note must be omitted (omitempty), not serialized as "".
	if strings.Contains(js, `"note"`) {
		t.Errorf("empty note should be omitted:\n%s", js)
	}
}
