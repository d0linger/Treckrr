package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// execPage renders a page's full "layout" with the given data and fails on any
// template execution error. This guards against the class of bug where a
// template compared a decimal.Decimal against a float literal (gt/lt), which
// parses fine but errors at render time — producing a 500 in production.
func execPage(t *testing.T, page string, data map[string]any) string {
	t.Helper()
	pages, err := Templates()
	if err != nil {
		t.Fatalf("Templates(): %v", err)
	}
	tpl, ok := pages[page]
	if !ok {
		t.Fatalf("page %q not registered", page)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("execute %q: %v", page, err)
	}
	return buf.String()
}

func TestStatsPageRendersWithPreviousYear(t *testing.T) {
	d := decimal.NewFromFloat
	rows := []map[string]any{{"Label": "Musterhof", "Hours": d(2.17), "Cost": d(209.88)}}
	// A completed year that HAS a previous year: exercises the comparison block
	// (the decimal-vs-float case that used to 500) and the completed-year payment
	// KPIs, including the Guthaben KPI asserted below.
	html := execPage(t, "stats", map[string]any{
		"Title":      "Statistik",
		"Year":       map[string]any{"Year": 2026, "ID": int64(3)},
		"TotalCost":  d(209.88),
		"TotalHours": d(2.17),
		"PaidCost":   d(0),
		"OpenCost":   d(209.88),
		"CreditCost": d(15), // exercises the Guthaben KPI branch
		"LedgerSum":  d(-30), "NetResult": d(179.88), "HasLedger": true,
		"Completed":  true,
		"ByNeighbor": rows, "ByNeighborMax": d(209.88),
		"ByTask": rows, "ByTaskMax": d(209.88),
		"ByTractor": rows, "ByTractorMax": d(209.88),
		"PrevYear": 2025, "PrevCost": d(150), "PrevHours": d(1.5),
		"DiffCost": d(59.88), "DiffUp": true, "DiffDown": false,
		"DiffPct": d(39.92), "DiffPctUp": true,
	})
	// A positive CreditCost must render the Guthaben KPI.
	if !strings.Contains(html, "Guthaben") {
		t.Errorf("stats page with a positive CreditCost should show the Guthaben KPI")
	}
}

func TestStatsAllPageRenders(t *testing.T) {
	d := decimal.NewFromFloat
	execPage(t, "stats_all", map[string]any{
		"Title": "Statistik – Alle Jahre",
		"Stats": []map[string]any{
			{"Year": 2026, "YearID": int64(3), "Cost": d(209.88), "Hours": d(2.17), "Ledger": d(-30), "Net": d(179.88), "PaidCost": d(0), "OpenCost": d(209.88), "Completed": false},
			{"Year": 2025, "YearID": int64(2), "Cost": d(150), "Hours": d(1.5), "Ledger": d(0), "Net": d(150), "PaidCost": d(150), "OpenCost": d(0), "Completed": true},
		},
		"Revenue":    []map[string]any{{"Label": "2026", "Hours": d(2.17), "Cost": d(209.88)}, {"Label": "2025", "Hours": d(1.5), "Cost": d(150)}},
		"RevenueMax": d(209.88),
		"GrandCost":  d(359.88), "GrandHours": d(3.67), "GrandPaid": d(150), "GrandOpen": d(209.88), "GrandCredit": d(15),
		"GrandLedger": d(-30), "GrandNet": d(329.88), "HasLedger": true,
	})
}

func TestBelegPageRenders(t *testing.T) {
	d := decimal.NewFromFloat
	// A day with several bookings (grouping + rail), a voided continuation row,
	// and the aggregated "Bündeln" view enabled — the paths the redesign added.
	html := execPage(t, "beleg", map[string]any{
		"Title":     "Beleg",
		"Neighbor":  map[string]any{"ID": int64(2), "Name": "Florian", "Address": "Dorf 1", "TaxID": "ATU55555555"},
		"Year":      map[string]any{"ID": int64(1), "Year": 2026, "Base": map[string]any{"Name": "Preisliste", "Year": 2026}},
		"TotalCost": d(498.19), "TotalHours": d(3.75),
		"Saldo": d(498.19), "LedgerSum": d(0),
		"Completed": false, "Paid": false, "Bookings": 3,
		"HasPayments": true, "PaidSum": d(300), "Remaining": d(198.19),
		"Payments": []map[string]any{
			{"PaidOn": time.Now(), "Amount": d(300), "Note": "Überweisung"},
		},
		"HasInvoice": true, "Rechnung": true,
		"Invoice": map[string]any{"Number": "2026-014", "IssuedOn": time.Now()},
		"Company": map[string]any{"Name": "Hof Bergmann", "Address": "Feldweg 3\n4780", "TaxID": "ATU123",
			"TaxNote": "§ 22 UStG", "TaxMode": "regel", "VATRate": d(13)},
		// Frozen §11 legal fields — deliberately DISTINCT from the live Company/Neighbor
		// above so the assertions prove the template renders the snapshot, not live data.
		"InvIssuer":    map[string]any{"Name": "Absender GmbH (fixiert)", "Address": "Altweg 9", "TaxID": "ATU-FIX-ISS", "IBAN": "AT00 FIXIERTE IBAN"},
		"InvRecipient": map[string]any{"Name": "Empfänger (fixiert)", "Address": "Rechnungsweg 2", "TaxID": "ATU-FIX-RCP"},
		"InvTaxNote":   "Fixierter Steuerhinweis § 22",
		"InvIBAN":      "AT00 FIXIERTE IBAN",
		"InvShowVAT":   true, "InvRate": d(13), "InvNet": d(647.60), "InvUSt": d(84.19),
		"InvBrutto": d(731.79), "InvLedger": d(-50), "InvPaidUSt": d(34.51), "InvRest": d(481.79),
		"InvNeedRecipientVATID": true,
		"Days": []map[string]any{
			{"Date": "09.05.", "Entries": []map[string]any{
				{"TaskLabel": "Mähen", "Unit": "h", "Hours": d(2.25), "HourlyRate": d(40), "Cost": d(251.19), "Voided": false},
				{"TaskLabel": "Schwadern groß", "Unit": "h", "Hours": d(1.5), "HourlyRate": d(52), "Cost": d(78), "Voided": false},
				{"TaskLabel": "Ballenpressen", "Unit": "Ballen", "Quantity": d(40), "UnitPrice": d(3.2), "Hours": d(0), "Cost": d(128), "Voided": false},
			}},
			{"Date": "10.05.", "Entries": []map[string]any{
				{"TaskLabel": "Schwadern groß", "Unit": "h", "Hours": d(1.5), "HourlyRate": d(52), "Cost": d(169), "Voided": false},
				// No task label, but a note → the note is the line description (B).
				{"TaskLabel": "", "Note": "Freie Sonderleistung", "Unit": "h", "Hours": d(1), "HourlyRate": d(10), "Cost": d(10), "Voided": false},
				{"TaskLabel": "", "Unit": "h", "Hours": d(0), "Cost": d(0), "Voided": true},
			}},
		},
		"CanBundle": true,
		"Groups": []map[string]any{
			{"Label": "Schwadern groß", "Count": 2, "Hours": d(3), "Cost": d(247)},
			{"Label": "Mähen", "Count": 1, "Hours": d(2.25), "Cost": d(251.19)},
		},
		"Bundle": true, "ShowGrund": true, "HasGrund": true,
		"GrundTractors": []map[string]any{
			{"Ident": "4095", "PS": "100", "Loads": []map[string]any{
				{"Load": "mittel", "CostPS": "0,40", "Rate": d(40), "Machines": []string{"Frontmähwerk", "Heckmähwerk"}},
			}},
			{"Ident": "9083", "PS": "94", "Loads": []map[string]any{
				{"Load": "leicht", "CostPS": "0,38", "Rate": d(35.72), "Machines": []string{"Kreiselzettwender"}},
				{"Load": "schwer", "CostPS": "0,42", "Rate": d(39.48), "Machines": []string{"Fräse"}},
			}},
		},
		"GrundMachines": []map[string]any{
			{"Name": "Frontmähwerk", "Width": "3,06", "CostAB": "14,00", "Rate": d(42.84)},
			{"Name": "Kreiselzettwender", "Width": "8,8", "CostAB": "5,00", "Rate": d(44)},
		},
		"Today": "30.07.2026",
	})
	// Assert the §11 invoice branches actually render, not just that the template
	// executes: recipient UID, the over-€10,000 UID reminder, and the booking-note
	// fallback for a label-less booking.
	for _, want := range []string{
		"Absender GmbH (fixiert)",                      // frozen issuer, not live Company name
		"ATU-FIX-ISS",                                  // frozen issuer UID
		"Empfänger (fixiert)",                          // frozen recipient
		"ATU-FIX-RCP",                                  // frozen recipient UID in the "An" block
		"Fixierter Steuerhinweis § 22",                 // frozen tax note
		"AT00 FIXIERTE IBAN",                           // frozen payment IBAN
		"UID/Steuernummer des Empfängers erforderlich", // soft § 11 reminder
		"Freie Sonderleistung",                         // note used instead of "Sonstige"
	} {
		if !strings.Contains(html, want) {
			t.Errorf("beleg HTML missing %q", want)
		}
	}
}

func TestInvoiceConfirmRenders(t *testing.T) {
	d := decimal.NewFromFloat
	base := func(canIssue bool, checks []map[string]any) map[string]any {
		return map[string]any{
			"Title":    "Festschreiben",
			"Neighbor": map[string]any{"ID": int64(2), "Name": "Florian"},
			"Year":     map[string]any{"ID": int64(1), "Year": 2026},
			"BackURL":  "/neighbors/2/beleg?year=1",
			"CanIssue": canIssue,
			"Content":  map[string]any{"ShowVAT": true, "Net": d(218), "VATRate": d(13), "VATAmount": d(28.34), "Gross": d(246.34)},
			"Checks":   checks,
		}
	}

	// Incomplete: a failing §11 item → checklist shown, no issue button.
	bad := execPage(t, "invoice_confirm", base(false, []map[string]any{
		{"Label": "Absender-Name", "Detail": "Hof Bergmann", "OK": true},
		{"Label": "Empfänger-Adresse", "Detail": "fehlt", "OK": false},
	}))
	for _, want := range []string{"§ 11 UStG", "Absender-Name", "Empfänger-Adresse", "nicht möglich"} {
		if !strings.Contains(bad, want) {
			t.Errorf("confirm(incomplete) missing %q", want)
		}
	}
	if strings.Contains(bad, "Jetzt festschreiben") {
		t.Errorf("incomplete confirm must not offer the festschreiben button")
	}

	// Complete: snapshot preview + issue button.
	ok := execPage(t, "invoice_confirm", base(true, []map[string]any{
		{"Label": "Absender-Name", "Detail": "Hof Bergmann", "OK": true},
	}))
	for _, want := range []string{"Snapshot-Vorschau", "Jetzt festschreiben", "246,34"} {
		if !strings.Contains(ok, want) {
			t.Errorf("confirm(complete) missing %q", want)
		}
	}
}

func TestStornoConfirmRenders(t *testing.T) {
	d := decimal.NewFromFloat
	html := execPage(t, "storno_confirm", map[string]any{
		"Title":    "Storno",
		"Neighbor": map[string]any{"ID": int64(2), "Name": "Florian"},
		"Year":     map[string]any{"ID": int64(1), "Year": 2026},
		"BackURL":  "/neighbors/2/beleg?year=1&rechnung=1",
		"Invoice":  map[string]any{"Number": "2026-002", "Content": map[string]any{"Gross": d(246.34)}},
	})
	// The reason input lives on the confirmation page, and the storno amount shows.
	for _, want := range []string{"2026-002", "246,34", `name="reason"`, "Storno ausstellen"} {
		if !strings.Contains(html, want) {
			t.Errorf("storno_confirm missing %q", want)
		}
	}
}

func TestComparePageRendersWithDiffs(t *testing.T) {
	d := decimal.NewFromFloat
	rows := []map[string]any{
		{"Label": "Mähen", "A": d(35.50), "B": d(30.00), "Diff": d(5.50), "Pct": d(18.33), "Up": true, "Down": false},
		{"Label": "Fräsen", "A": d(28.00), "B": d(31.00), "Diff": d(-3.00), "Pct": d(-9.68), "Up": false, "Down": true},
	}
	execPage(t, "compare", map[string]any{
		"Title":   "Vergleich",
		"Base":    map[string]any{"ID": int64(1), "Year": 2026, "Name": "Grundlage 2026"},
		"Against": map[string]any{"ID": int64(2), "Year": 2023, "Name": "Grundlage 2023"},
		"Bases": []map[string]any{
			{"ID": int64(1), "Year": 2026, "Name": "Grundlage 2026"},
			{"ID": int64(2), "Year": 2023, "Name": "Grundlage 2023"},
		},
		"GespannDiffs": rows, "MachineDiffs": rows, "LoadDiffs": rows,
	})
}

func TestCompanyPageRenders(t *testing.T) {
	d := decimal.NewFromFloat
	execPage(t, "company", map[string]any{
		"Title": "Betriebsdaten",
		"Company": map[string]any{
			"Name": "Hof Bergmann", "Address": "Feldweg 3\n4780 Schärding", "TaxID": "ATU12345678",
			"TaxNote": "§ 22 UStG", "TaxMode": "pauschal", "VATRate": d(0),
		},
	})
}

func TestBackupPageRenders(t *testing.T) {
	// Configured "ok" state exercises the date/IsZero/size/offhost branch.
	execPage(t, "backup", map[string]any{
		"Title": "Backup",
		"Backup": map[string]any{
			"Enabled": true, "Configured": true, "State": "ok",
			"LastBackup": time.Now().Add(-3 * time.Hour), "AgeHours": 3,
			"SizeLabel": "4.2 MB", "Offhost": "ok",
			"Encrypted": true, "SchemaVersion": "0021_neighbor_tax_id.sql",
			"RestoreTested": time.Now().Add(-3 * time.Hour),
		},
		"Settings":       map[string]any{"VolumeCron": "0 3 * * *", "VolumeKeep": 7, "S3Cron": "0 4 * * *", "S3Keep": 0},
		"VolumeCronDesc": "Täglich um 03:00 Uhr.",
		"S3CronDesc":     "Täglich um 04:00 Uhr.",
		"NextVolume":     "02.08.2026 03:00",
		"NextS3":         "02.08.2026 04:00",
		"Files":          []map[string]any{{"Name": "treckrr-2026-08-01-030000.dump.enc", "Size": "57 KB", "ModTime": time.Now()}},
		// Exercise the "+ N weitere" collapse branches for both lists.
		"FilesMore":   []map[string]any{{"Name": "treckrr-2026-07-31-030000.dump.enc", "Size": "56 KB", "ModTime": time.Now()}},
		"S3Enabled":   true,
		"S3Bucket":    "s3-dp",
		"S3Files":     []map[string]any{{"Name": "treckrr-2026-08-01-040000.dump.enc", "Size": "57 KB", "ModTime": time.Now()}},
		"S3FilesMore": []map[string]any{{"Name": "treckrr-2026-07-31-040000.dump.enc", "Size": "56 KB", "ModTime": time.Now()}},
	})
}
