package web

import (
	"bytes"
	"html"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
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
		"Bundle": true, "ShowGrund": true, "HasGrund": true, "GrundCatalogReference": true,
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
		"Aktuelle Katalog-Referenzwerte; gebuchte Verrechnungssätze stehen in den Positionen.",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("beleg HTML missing %q", want)
		}
	}
}

// TestBelegLedgerDescriptionPrefixesOnlyLegacyPayables distinguishes manual
// account postings from structured incoming bookings without losing void state.
func TestBelegLedgerDescriptionPrefixesOnlyLegacyPayables(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	page := html.UnescapeString(execPage(t, "beleg", map[string]any{
		"Title":      "Beleg",
		"Neighbor":   models.Neighbor{ID: 2, Name: "Bio-Hof Steiner"},
		"Year":       models.BillingYear{ID: 1, Year: 2026},
		"TotalCost":  decimal.Zero,
		"TotalHours": decimal.Zero,
		"Ledger": []models.LedgerEntry{
			{Date: time.Now(), Description: "Legacy payable", Amount: d("-10")},
			{Date: time.Now(), Description: "Legacy receivable", Amount: d("10")},
			{Date: time.Now(), Amount: d("-24"), Booking: &models.LedgerBooking{
				Version: 1, Kind: "equipment", TaskLabel: "Incoming equipment",
				Unit: "h", Quantity: d("2"), UnitPrice: d("12"),
			}},
			{Date: time.Now(), Amount: d("-6"), Voided: true, Booking: &models.LedgerBooking{
				Version: 1, Kind: "equipment", TaskLabel: "Voided incoming",
				Unit: "h", Quantity: d("1"), UnitPrice: d("6"),
			}},
		},
		"LedgerSum": d("-30"),
		"Saldo":     d("-30"),
		"Today":     "23.09.2026",
	}))

	if count := strings.Count(page, "Ich schulde · "); count != 1 {
		t.Fatalf("legacy payable prefix count = %d, want 1", count)
	}
	for _, want := range []string{
		"Ich schulde · Legacy payable",
		"Legacy receivable",
		"Incoming equipment · 2 h × 12 €",
		"Voided incoming · 1 h × 6 € · storniert",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("beleg ledger missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Ich schulde · Legacy receivable",
		"Ich schulde · Incoming equipment",
		"Ich schulde · Voided incoming",
	} {
		if strings.Contains(page, unwanted) {
			t.Errorf("beleg ledger unexpectedly contains %q", unwanted)
		}
	}
}

// TestBelegPageEndsLedgerWithActualSaldo keeps the printable statement from
// stopping at the ledger subtotal when the actual amount is services plus ledger.
func TestBelegPageEndsLedgerWithActualSaldo(t *testing.T) {
	d := decimal.RequireFromString
	page := execPage(t, "beleg", map[string]any{
		"Title":      "Beleg",
		"Neighbor":   models.Neighbor{ID: 2, Name: "Bio-Hof Steiner"},
		"Year":       models.BillingYear{ID: 1, Year: 2026},
		"TotalCost":  d("2210.35"),
		"TotalHours": d("13.5"),
		"Ledger": []models.LedgerEntry{{
			Date:        time.Date(2026, time.September, 21, 0, 0, 0, 0, time.Local),
			Description: "Betonmischen", Amount: d("-80.80"),
		}},
		"LedgerSum": d("-80.80"),
		"Saldo":     d("2129.55"),
		"Bookings":  6,
		"Today":     "21.09.2026",
	})

	marker := `data-beleg-final-total`
	if strings.Count(page, marker) != 1 {
		t.Fatalf("final total rendered %d times, want once", strings.Count(page, marker))
	}
	markerIndex := strings.Index(page, marker)
	start := strings.LastIndex(page[:markerIndex], "<div")
	if start < 0 {
		t.Fatal("final total marker is not inside a div")
	}
	end := min(start+400, len(page))
	finalTotal := page[start:end]
	for _, want := range []string{"beleg__lsub--total", "Saldo", "2.129,55 €"} {
		if !strings.Contains(finalTotal, want) {
			t.Errorf("final statement total missing %q", want)
		}
	}
}

func TestIncomingBookingBreakdownRendersInBothOverviews(t *testing.T) {
	t.Parallel()
	booking := &models.LedgerBooking{
		Version: 1, Kind: "equipment", TaskLabel: "Beton mischen", Note: "Nordfeld",
		Unit: "h", Quantity: decimal.NewFromInt(4), UnitPrice: decimal.RequireFromString("55.80"),
		PartnerLabel: "Fixes Gespann", People: []models.BookingPerson{{
			ID: 1, Name: "Daniel", Hours: decimal.NewFromInt(4), Rate: decimal.NewFromInt(25),
		}},
	}
	ledger := models.LedgerEntry{
		ID: 9, Amount: booking.Total().Neg(), Description: "flattened legacy description",
		Date: time.Date(2026, time.September, 21, 0, 0, 0, 0, time.UTC), Booking: booking,
	}
	year := models.BillingYear{ID: 7, Year: 2026, Status: models.YearCompleted}
	neighbor := models.Neighbor{ID: 3, Name: "Bio-Hof Steiner"}

	page := execPage(t, "neighbor", map[string]any{
		"Title": "Bio-Hof Steiner", "Year": year, "Base": models.PriceBase{ID: 1}, "Neighbor": neighbor,
		"Completed": true, "Ledger": []models.LedgerEntry{ledger}, "LedgerSum": ledger.Amount,
		"Saldo": ledger.Amount, "Remaining": ledger.Amount, "CreditAmount": ledger.Amount.Neg(),
		"TotalCost": decimal.Zero, "TotalHours": decimal.Zero, "BookingCount": 1, "PaidSum": decimal.Zero,
		"Stale": map[int64]bool{}, "PhotoCounts": map[int64]int{}, "LedgerPhotoCounts": map[int64]int{9: 1},
		"PairLabel": map[int64]string{}, "LinkedFrom": map[int64]int64{},
	})
	for _, want := range []string{
		"Meine Leistungen", "Nachbarleistungen &amp; Verrechnung", "Alle Buchungen · beide Richtungen",
		"Beton mischen", "Maschinenleistung · Fixes Gespann · 4 h × 55,80 €/h", "223,20 €",
		"Mannstunden · Daniel · 4 h × 25,00 €/h", "100,00 €", "1 Foto",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("neighbor overview missing %q", want)
		}
	}
	if strings.Contains(page, ledger.Description) {
		t.Error("structured booking fell back to its flattened ledger description")
	}

	row := store.BookingRow{
		EntryRow: store.EntryRow{Entry: models.Entry{
			ID: ledger.ID, NeighborID: neighbor.ID, BillingYearID: year.ID, Date: ledger.Date,
			TaskLabel: booking.TaskLabel, Note: booking.Note, Unit: booking.Unit, Hours: booking.Quantity,
			Quantity: booking.Quantity, UnitPrice: booking.UnitPrice, Cost: ledger.Amount,
		}, NeighborName: neighbor.Name},
		Source: "ledger", Direction: "in", Kind: "equipment", Booking: booking,
	}
	list := execPage(t, "entries", map[string]any{
		"Title": "Buchungen", "Year": year, "Rows": []store.BookingRow{row}, "Total": 1,
		"SumCost": ledger.Amount, "Filter": map[string]string{}, "Units": []string{"h"},
		"PhotoCounts": map[int64]int{}, "LedgerPhotoCounts": map[int64]int{9: 1},
		"Page": 1, "Pages": 1,
	})
	for _, want := range []string{"Maschinenleistung · 4 h", "Mannstunden · Daniel · 4 h", "Maschine 223,20 €", "Daniel 100,00 €", "1 Foto"} {
		if !strings.Contains(list, want) {
			t.Errorf("combined booking overview missing %q", want)
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
	for _, want := range []string{"Rechnungsvorschau", "Jetzt festschreiben", "246,34"} {
		if !strings.Contains(ok, want) {
			t.Errorf("confirm(complete) missing %q", want)
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
			"PaymentTermDays": 14, "DunningGraceDays": 14,
			"DunningFee1": d(0), "DunningFee2": d(0),
			"SkontoPct": d(0), "SkontoDays": 0,
			"InvoicePrefix": "", "InvoiceStart": 1, "SmallBusinessLimit": d(0),
			"TravelFlat": d(0), "TravelPerKm": d(0),
			"MailSignature": "", "MailCC": "",
		},
	})
}

func TestYearClosingRenders(t *testing.T) {
	// A clean year and an open one, so both branches of every check render.
	execPage(t, "year_closing", map[string]any{
		"Title": "Jahresabschluss",
		"Year":  map[string]any{"ID": int64(1), "Year": 2026, "Status": "in_progress"},
		// Real store values, not maps: the template calls .Clean and .More, and a
		// map would silently answer nil for both — rendering only one branch.
		"Checks": []store.ClosingCheck{
			{Key: "uninvoiced", Label: "Buchungen ohne Rechnung", Detail: "d"},
			{Key: "unpaid", Label: "Offene Beträge", Detail: "d",
				Count: 7, Names: []string{"Huber", "Maier"}, Amount: decimal.NewFromInt(240)},
		},
		"OpenChecks": 1,
	})
}

func TestRechnungsjournalRenders(t *testing.T) {
	// Empty state (no documents yet) — the rich path runs in the integration test.
	execPage(t, "rechnungsjournal", map[string]any{
		"Title": "Rechnungsjournal",
		"Year":  map[string]any{"ID": int64(1), "Year": 2026},
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
			"RestoreTested":   time.Now().Add(-3 * time.Hour),
			"ArchiveVerified": time.Now().Add(-1 * time.Hour),
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

// TestProfileSessionDisclosurePreservesControls checks collapse above five sessions,
// per-session revoke targets, and global account actions outside the disclosure.
func TestProfileSessionDisclosurePreservesControls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		count     int
		collapsed bool
	}{
		{name: "empty", count: 0},
		{name: "current_session_only", count: 1},
		{name: "five_sessions_expanded", count: 5},
		{name: "six_sessions_collapsed", count: 6, collapsed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessions := make([]models.Session, tc.count)
			for i := range sessions {
				sessions[i] = models.Session{
					Token:     "test-session-" + strconv.Itoa(i),
					UserAgent: "Test Browser " + strconv.Itoa(i),
					IP:        "192.0.2." + strconv.Itoa(i+1),
					LastSeen: time.Date(
						2026,
						time.September,
						1,
						12,
						0,
						0,
						0,
						time.UTC,
					),
					Current: i == 0,
				}
			}
			page := execPage(t, "profile", map[string]any{
				"Title":    "Einstellungen",
				"User":     models.User{Username: "test-user", Role: models.RoleEditor},
				"Sessions": sessions,
			})

			const disclosure = `<details class="disclosure session-disclosure">`
			if got := strings.Contains(page, disclosure); got != tc.collapsed {
				t.Fatalf("collapsed session disclosure = %v, want %v", got, tc.collapsed)
			}
			controls := page
			outside := page
			if tc.collapsed {
				before, after, _ := strings.Cut(page, disclosure)
				var closed bool
				controls, outside, closed = strings.Cut(after, "</details>")
				if !closed {
					t.Fatal("session disclosure is not closed")
				}
				outside = before + outside
				if !strings.Contains(controls, "Alle aktiven Sitzungen anzeigen") {
					t.Error("collapsed session list has no visible expansion control")
				}
			}
			for _, session := range sessions {
				if got := strings.Count(controls, session.IP+"</span>"); got != 1 {
					t.Errorf("session %q rendered %d times, want once", session.IP, got)
				}
			}
			wantCurrent := min(tc.count, 1)
			if got := strings.Count(controls, "Diese Sitzung"); got != wantCurrent {
				t.Errorf("current-session labels = %d, want %d", got, wantCurrent)
			}

			// Match each revoke form with its token, not merely the aggregate button count.
			revokeForms := regexp.MustCompile(
				`<form method="post" action="/account/sessions/revoke">\s*`+
					`<input type="hidden" name="token" value="([^"]+)">`,
			).FindAllStringSubmatch(controls, -1)
			wantRevokes := max(tc.count-1, 0)
			if len(revokeForms) != wantRevokes {
				t.Fatalf("individual revoke forms = %d, want %d", len(revokeForms), wantRevokes)
			}
			if got := strings.Count(controls, `type="submit">Beenden</button>`); got != wantRevokes {
				t.Errorf("individual revoke buttons = %d, want %d", got, wantRevokes)
			}
			for i, form := range revokeForms {
				if want := sessions[i+1].Token; form[1] != want {
					t.Errorf("revoke form token = %q, want %q", form[1], want)
				}
			}
			if strings.Contains(controls, `name="token" value="test-session-0"`) {
				t.Error("current session must not have an individual revoke control")
			}
			for _, action := range []string{"/account/sessions/revoke-others", "/logout"} {
				if !strings.Contains(outside, `method="post" action="`+action+`"`) {
					t.Errorf("global action %q must remain outside the collapsed session list", action)
				}
			}
		})
	}
}

// TestMahnungPagePreservesDocumentAndPaymentActions checks reminder content,
// optional payment details, and correctly scoped PDF, QR, and email actions across stages.
func TestMahnungPagePreservesDocumentAndPaymentActions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		stage                  int
		fee, paid              int64
		wantTotal              string
		dates, qr, mailEnabled bool
		email                  string
		wantEmail              bool
	}{
		{name: "reminder_without_optional_details", stage: 0, wantTotal: "100,00 €"},
		{
			name: "first_reminder_with_payment_details", stage: 1, fee: 5, paid: 20,
			wantTotal: "105,00 €", dates: true, qr: true,
			mailEnabled: true, email: "neighbor@example.invalid", wantEmail: true,
		},
		{
			name: "second_reminder_without_recipient_email", stage: 2, fee: 12,
			wantTotal: "112,00 €", mailEnabled: true,
		},
		{
			name: "email_disabled_with_recipient_address", stage: 1, fee: 5,
			wantTotal: "105,00 €", email: "neighbor@example.invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			date := time.Date(
				2026,
				time.September,
				1,
				12,
				0,
				0,
				0,
				time.UTC,
			)
			data := map[string]any{
				"Title":      models.DunningStageTitle(tc.stage),
				"Intro":      "Bitte begleichen Sie den offenen Betrag.",
				"Stage":      tc.stage,
				"Year":       models.BillingYear{ID: 7, Year: 2026},
				"Neighbor":   models.Neighbor{ID: 12, Name: "Testnachbar", Address: "Feldweg 2"},
				"Company":    models.Company{Name: "Hof <Bergmann>", Address: "Dorfweg 1", IBAN: "TEST-IBAN"},
				"InvoiceNo":  "2026-012",
				"Today":      date,
				"IssuedOn":   time.Time{},
				"Open":       decimal.NewFromInt(100),
				"Paid":       decimal.NewFromInt(tc.paid),
				"Fee":        decimal.NewFromInt(tc.fee),
				"TotalDue":   decimal.NewFromInt(100 + tc.fee),
				"GraceUntil": time.Time{},
				"HasEpcQR":   tc.qr, "MailEnabled": tc.mailEnabled, "NeighborEmail": tc.email,
			}
			if tc.dates {
				data["IssuedOn"] = date.AddDate(0, 0, -21)
				data["DueOn"] = date.AddDate(0, 0, -7)
				data["GraceUntil"] = date.AddDate(0, 0, 10)
			}
			page := execPage(t, "mahnung", data)
			for _, want := range []string{
				`class="beleg beleg--rechnung"`,
				"Hof &lt;Bergmann&gt;", "Dorfweg 1", "Testnachbar", "Feldweg 2",
				models.DunningStageTitle(tc.stage), "Rechnung Nr. 2026-012",
				"Offener Betrag", "100,00 €", "<strong>" + tc.wantTotal + "</strong>",
				"TEST-IBAN", `href="/mahnwesen?year=7"`,
			} {
				if !strings.Contains(page, want) {
					t.Errorf("reminder HTML missing %q", want)
				}
			}
			for _, optional := range []struct {
				text string
				want bool
			}{
				{text: "Mahnspesen", want: tc.fee > 0},
				{text: "bereits bezahlt 20,00 €", want: tc.paid > 0},
				{text: "Rechnung vom 11.08.2026", want: tc.dates},
				{text: "fällig war 25.08.2026", want: tc.dates},
				{text: "bis <strong>11.09.2026</strong>", want: tc.dates},
				{text: `class="beleg__epcqr"`, want: tc.qr},
				{text: "Per E-Mail senden", want: tc.wantEmail},
			} {
				if got := strings.Contains(page, optional.text); got != optional.want {
					t.Errorf(
						"optional content %q present = %v, want %v",
						optional.text,
						got,
						optional.want,
					)
				}
			}
			// HTML escaping of query separators must not obscure the actual action URL.
			decoded := html.UnescapeString(page)
			query := "?year=7&stufe=" + strconv.Itoa(tc.stage)
			if !strings.Contains(decoded, `href="/neighbors/12/mahnung.pdf`+query+`"`) {
				t.Error("PDF link does not preserve neighbor, year, and reminder stage")
			}
			if tc.qr && !strings.Contains(decoded, `src="/neighbors/12/mahnung/epc-qr.png`+query+`"`) {
				t.Error("EPC QR link does not preserve neighbor, year, and reminder stage")
			}
			emailAction := `method="post" action="/neighbors/12/mahnung/email` + query + `"`
			if got := strings.Contains(decoded, emailAction); got != tc.wantEmail {
				t.Errorf("correctly targeted email form present = %v, want %v", got, tc.wantEmail)
			}
		})
	}
}

// TestDashboardShowsCreditAsOwedNotPaid pins the reverse of "offene Zahlung":
// a neighbor with a negative rest (Guthaben) is money I still owe. A completed
// year must list it under "Zu erledigen" and in the status row, and its tile
// must say Guthaben — never Bezahlt.
func TestDashboardShowsCreditAsOwedNotPaid(t *testing.T) {
	d := decimal.NewFromFloat
	page := execPage(t, "dashboard", map[string]any{
		"User":      models.User{ID: 1, Username: "admin", Role: models.RoleAdmin},
		"Year":      models.BillingYear{ID: 5, Year: 2026, Base: &models.PriceBase{Year: 2026}},
		"Completed": true,
		"GrandCost": d(-60), "GrandHours": decimal.Zero,
		"PaidCost": decimal.Zero, "OpenCost": decimal.Zero,
		"CreditCount": 1, "CreditCost": d(60),
		"Summaries": []map[string]any{{
			"Neighbor": models.Neighbor{ID: 9, Name: "Demo-Hof Leitner"},
			"Cost":     d(-60), "Hours": decimal.Zero, "Entries": 0,
			"Paid": false, "Credit": true, "Remaining": d(-60),
		}},
	})
	for _, want := range []string{
		"mit Guthaben – noch auszuzahlen · 60,00 €",
		"Guthaben 60,00 €",
		"Guthaben · 60,00 €",
	} {
		if !strings.Contains(html.UnescapeString(page), want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(page, "paychip--paid") {
		t.Error("a Guthaben tile must not render the Bezahlt chip")
	}
}

// TestDashboardDueRowsTargetMatchingTiles pins the "Zu erledigen" → tile ring:
// each row jumps to its own anchor, and only unsettled tiles carry the matching
// data-due marker (the CSS :has(:target) rule keys on both).
func TestDashboardDueRowsTargetMatchingTiles(t *testing.T) {
	d := decimal.NewFromFloat
	tile := func(id int64, paid, credit bool, rest float64) map[string]any {
		return map[string]any{
			"Neighbor": models.Neighbor{ID: id, Name: "Hof " + strconv.FormatInt(id, 10)},
			"Cost":     d(rest), "Hours": decimal.Zero, "Entries": 1,
			"Paid": paid, "Credit": credit, "Remaining": d(rest),
		}
	}
	page := execPage(t, "dashboard", map[string]any{
		"User":      models.User{ID: 1, Username: "admin", Role: models.RoleAdmin},
		"Year":      models.BillingYear{ID: 5, Year: 2026, Base: &models.PriceBase{Year: 2026}},
		"Completed": true,
		"GrandCost": d(40), "GrandHours": decimal.Zero,
		"PaidCost": decimal.Zero, "OpenCost": d(100),
		"OpenCount": 1, "CreditCount": 1, "CreditCost": d(60),
		"Summaries": []map[string]any{tile(1, false, false, 100), tile(2, false, true, -60), tile(3, true, false, 0)},
	})
	for _, want := range []string{
		`href="#offene-zahlungen"`, `id="offene-zahlungen"`,
		`href="#auszuzahlen"`, `id="auszuzahlen"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard missing %s", want)
		}
	}
	if n := strings.Count(page, `data-due="open"`); n != 1 {
		t.Errorf(`data-due="open" tiles = %d, want 1`, n)
	}
	if n := strings.Count(page, `data-due="credit"`); n != 1 {
		t.Errorf(`data-due="credit" tiles = %d, want 1`, n)
	}
}
