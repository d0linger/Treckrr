package web

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/net/html"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestReadOnlyPagesPreserveReadsWithoutMutationControls exercises populated
// branches as both roles; hiding an empty form alone must not satisfy the test.
func TestReadOnlyPagesPreserveReadsWithoutMutationControls(t *testing.T) {
	t.Parallel()
	d := decimal.NewFromInt
	day := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	neighbor := models.Neighbor{ID: 2, Name: "Testhof", Address: "Dorf 1", Email: "test@example.invalid"}
	base := models.PriceBase{ID: 3, Year: 2026, Name: "Testbasis"}
	year := models.BillingYear{ID: 1, Year: 2026, Base: &base}
	common := map[string]any{
		"Year": year, "Base": base, "Neighbor": neighbor, "EditNeighborID": int64(2),
		"TotalCost": d(100), "TotalHours": d(2), "Saldo": d(100), "LedgerSum": d(0),
		"PaidSum": d(0), "Remaining": d(100), "GrandCost": d(100), "GrandHours": d(2),
		"Company": models.Company{}, "Invoice": models.Invoice{ID: 4, Number: "2026-001", IssuedOn: day},
		"InvIssuer": models.InvoiceParty{}, "InvRecipient": models.InvoiceParty{},
		"InvBrutto": d(100), "InvRest": d(100), "AnzahlungSum": d(0),
		"MailEnabled": true, "NeighborEmail": neighbor.Email, "Today": day,
		"Back": "/", "BackURL": "/", "Title": "Test",
	}
	tests := []struct {
		name, page, writerAction, readable string
		data                               map[string]any
	}{
		{"dashboard", "dashboard", "/years/remove-neighbor", `/export/year/1`, map[string]any{
			"Summaries": []map[string]any{{"Neighbor": neighbor, "Entries": 0, "Hours": d(0), "Cost": d(0), "PaidAmount": d(0), "Remaining": d(0)}},
			"Available": []models.Neighbor{neighbor}, "PrevNeighbors": []models.Neighbor{neighbor},
		}},
		{"years", "years", "/years/1/update", `href="/?year=1"`, map[string]any{
			"Rows":  []map[string]any{{"ID": int64(1), "Year": 2026, "Base": "Testbasis", "Entries": 0, "BaseID": int64(3), "Completed": true}},
			"Bases": []models.PriceBase{base}, "HasBases": true,
		}},
		{"neighbors", "neighbors_manage", "/neighbors/2/archive", `/neighbors/2/dsgvo-export.json`, map[string]any{
			"Stats": []map[string]any{{"Neighbor": neighbor, "Years": 1, "Entries": 1}, {"Neighbor": models.Neighbor{ID: 5, Name: "Anonym", Anonymized: true}, "Years": 1, "Entries": 1}},
		}},
		{"bases", "bases", "/bases/3/update", `/prices?base=3`, map[string]any{
			"BaseViews": []map[string]any{{"Base": base}}, "Bases": []models.PriceBase{base},
		}},
		{"combinations", "gespanne", "/gespanne/7/delete", "Testgespann", map[string]any{
			"Views": []map[string]any{{"Gespann": models.Gespann{ID: 7, Name: "Testgespann"}, "Rate": d(50)}},
		}},
		{"price fuel", "prices", "/prices/fuel-adjustments/7/delete", "Testzuschlag", map[string]any{
			"EditMode": true, "PriceSection": "fuel", "FuelAdjustments": []map[string]any{{"ID": 7, "Label": "Testzuschlag", "EffectiveFrom": day, "AmountPerH": d(2)}},
			"MachineViews": []map[string]any{}, "TractorViews": []map[string]any{}, "Loads": []models.LoadLevel{},
		}},
		{"price loads", "prices", "/prices/loadlevels/7/delete", "Testlast", map[string]any{
			"EditMode": true, "PriceSection": "loads", "Loads": []models.LoadLevel{{ID: 7, Name: "Testlast", CostPerPS: d(2)}},
			"MachineViews": []map[string]any{}, "TractorViews": []map[string]any{},
		}},
		{"price tractors", "prices", "/prices/tractors/7/delete", "Testtraktor", map[string]any{
			"EditMode": true, "PriceSection": "tractors", "TractorViews": []map[string]any{{"Tractor": models.Tractor{ID: 7, Name: "Testtraktor", PS: d(100), Active: true}}},
			"MachineViews": []map[string]any{}, "Loads": []models.LoadLevel{},
		}},
		{"price machines", "prices", "/prices/machines/7/delete", "Testmaschine", map[string]any{
			"EditMode": true, "PriceSection": "machines", "MachineViews": []map[string]any{{"Machine": models.Machine{ID: 7, Name: "Testmaschine", Active: true}, "Rate": d(50)}},
			"TractorViews": []map[string]any{}, "Loads": []models.LoadLevel{},
		}},
		{"persons", "persons", "/personen/7/update", "Testhelfer", map[string]any{
			"Persons": []models.Person{{ID: 7, Name: "Testhelfer", HourlyRate: d(25)}},
		}},
		{"closing", "year_closing", "/years/1/status", "Alle Prüfungen anzeigen", nil},
		{"rollover", "year_rollover", "/years/1/status", `/years/1/abschluss`, map[string]any{
			"Source": year, "TargetYear": 2027, "OpenAmount": d(0), "CreditAmount": d(0),
		}},
		{"recurring", "recurring", "/recurring/7/update", "Nächste Termine", map[string]any{
			"Rules": []map[string]any{{"ID": 7, "NeighborName": neighbor.Name, "NextRun": day, "Active": true, "Template": map[string]any{"Summary": "Testserie"}}},
		}},
		{"payment", "payment_edit", "/payments/7/update", `value="50"`, map[string]any{
			"Payment": models.Payment{ID: 7, NeighborID: 2, BillingYearID: 1, PaidOn: day, Created: day, Amount: d(50)},
		}},
		{"legacy ledger", "ledger_edit", "/ledger/7/update", `value="50.00"`, map[string]any{
			"Ledger": models.LedgerEntry{ID: 7, Date: day, Description: "Testverrechnung"}, "AbsAmount": d(50),
		}},
		{"recalculation", "recalc", "/years/1/recalc", "Differenz", map[string]any{
			"Count": 1, "Diff": d(1), "OldTotal": d(100), "NewTotal": d(101), "ApplyURL": "/years/1/recalc",
		}},
		{"carry forward", "carry_forward", "/neighbors/2/carry-forward", "Salden vor und nach", map[string]any{
			"Preview": map[string]any{"Source": d(100), "Target": d(0), "TargetAfter": d(100)},
		}},
		{"invoice confirmation", "invoice_confirm", "/neighbors/2/invoice", "Rechnungsvorschau", map[string]any{
			"CanIssue": true, "Content": models.InvoiceContent{Gross: d(100)},
		}},
		{"account document", "beleg", "/neighbors/2/anzahlung", "data-beleg-print", nil},
		{"invoice document legacy", "beleg", "/neighbors/2/beleg/mark-sent", "data-beleg-copy", map[string]any{
			"HasInvoice": true, "HasActiveInvoices": true,
		}},
		{"reminders", "mahnwesen", "/mahnwesen/batch-email", "/mahnwesen/export.csv", map[string]any{
			"Rows":        []map[string]any{{"NeighborID": 2, "InvoiceID": int64(4), "Name": neighbor.Name, "InvoiceNo": "2026-001", "DueOn": day, "Open": d(100), "DaysOverdue": 2}},
			"LastNotices": map[int64]any{},
		}},
		{"reminder document", "mahnung", "/neighbors/2/mahnung/email", "/neighbors/2/mahnung.pdf", map[string]any{
			"Open": d(100), "Paid": d(0), "TotalDue": d(100), "Fee": d(0), "IssuedOn": day, "GraceUntil": day,
		}},
		{"booking import", "import", "/entries/import/preview", "/entries/import/sample.csv", nil},
		{"payment import", "payment_import", "/payments/import/preview", "/payments/import/batches/7", map[string]any{
			"Batches": []map[string]any{{"ID": 7, "CreatedAt": day, "Status": "completed"}},
		}},
		{"payment import report", "payment_import_batch", "/payments/import/rows/8/reverse", "/payments/import/batches/7/report.csv", map[string]any{
			"Batch": map[string]any{"ID": 7, "CreatedAt": day}, "Rows": []map[string]any{{"ID": 8, "Amount": d(50), "Status": "booked", "Correctable": true}},
		}},
		{"notifications", "notifications", "/notifications/7/dismiss", "/account/notifications", map[string]any{
			"Notifications": []map[string]any{{"ID": 7, "Title": "Testhinweis", "CreatedAt": day}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := maps.Clone(common)
			maps.Copy(data, tc.data)
			data["User"] = &models.User{Role: models.RoleEditor}
			writer := mainContent(t, execPage(t, tc.page, data))
			if !strings.Contains(writer, `action="`+tc.writerAction) {
				t.Fatalf("writer fixture does not exercise %q", tc.writerAction)
			}
			data["User"] = &models.User{Role: models.RoleViewer}
			reader := mainContent(t, execPage(t, tc.page, data))
			if !strings.Contains(reader, tc.readable) {
				t.Errorf("read-only view lost %q", tc.readable)
			}
			assertReadOnlyForms(t, reader)
		})
	}
}

// TestReadOnlyBookingListNoticeIncludesEmptyResults shows the read-only notice
// once on empty and populated booking lists, keeping filters and CSV export.
func TestReadOnlyBookingListNoticeIncludesEmptyResults(t *testing.T) {
	t.Parallel()
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "populated"}[populated], func(t *testing.T) {
			t.Parallel()
			var rows []store.BookingRow
			if populated {
				rows = []store.BookingRow{{Source: "entry", Kind: "equipment", EntryRow: store.EntryRow{
					Entry: models.Entry{ID: 3, TaskLabel: "Heuernte", Cost: decimal.NewFromInt(25)},
				}}}
			}
			page := mainContent(t, execPage(t, "entries", map[string]any{
				"User": models.User{Role: models.RoleViewer}, "Year": models.BillingYear{ID: 1},
				"Filter": map[string]string{}, "SumCost": decimal.Zero, "Pages": 1,
				"PhotoCounts": map[int64]int{}, "Rows": rows, "HasEntryRows": populated,
			}))
			if strings.Count(page, "data-readonly-notice") != 1 || !strings.Contains(page, "Nur-Lese-Zugriff") {
				t.Error("booking list must explain read-only access once, even with no results")
			}
			if !strings.Contains(page, "Gefilterte Liste als CSV") || !strings.Contains(page, `action="/buchungen"`) {
				t.Error("read-only booking list lost export or filters")
			}
			assertReadOnlyForms(t, page)
		})
	}
}

// TestReadOnlyMachineCostAssumptionsRemainVisible shows viewers every stored
// machine cost assumption and the computed proposal as read-only values.
func TestReadOnlyMachineCostAssumptionsRemainVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		role   string
		locked bool
	}{
		{models.RoleViewer, false}, {models.RoleEditor, true}, {models.RoleAdmin, true},
	} {
		t.Run(tc.role, func(t *testing.T) {
			d := decimal.NewFromInt
			page := mainContent(t, execPage(t, "prices", map[string]any{
				"User": &models.User{Role: tc.role}, "Locked": tc.locked, "EditMode": true, "PriceSection": "machines",
				"Base": models.PriceBase{ID: 1, Year: 2026},
				"MachineViews": []map[string]any{{
					"Machine": models.Machine{
						ID: 7, Name: "Testmaschine", Active: true, SortOrder: 3,
						SelfCostPerH: d(18), AcquisitionCost: d(12000), ResidualValue: d(2000),
						UsefulYears: 8, AnnualHours: d(500), FuelCostPerH: d(9),
						AnnualMaintenance: d(800), AnnualInsurance: d(300), AnnualOtherCost: d(200),
					},
					"Rate": d(50), "HasProposal": true, "Proposal": decimal.RequireFromString("14.10"),
				}},
				"TractorViews": []map[string]any{}, "Loads": []models.LoadLevel{},
			}))
			for _, row := range []string{
				`<span>Reihenfolge</span><strong>3</strong>`,
				`<span>Selbstkosten</span><strong>18,00 €/h</strong>`,
				`<span>Anschaffungskosten</span><strong>12.000,00 €</strong>`,
				`<span>Restwert</span><strong>2.000,00 €</strong>`,
				`<span>Nutzungsdauer</span><strong>8 Jahre</strong>`,
				`<span>Einsatzstunden pro Jahr</span><strong>500 h</strong>`,
				`<span>Treibstoff</span><strong>9,00 €/h</strong>`,
				`<span>Wartung</span><strong>800,00 €/Jahr</strong>`,
				`<span>Versicherung</span><strong>300,00 €/Jahr</strong>`,
				`<span>Sonstige Kosten</span><strong>200,00 €/Jahr</strong>`,
				`<span>Berechneter Vorschlag</span><strong>14,10 €/h</strong>`,
			} {
				if !strings.Contains(page, row) {
					t.Errorf("read-only cost model lost stored value %s", row)
				}
			}
			assertReadOnlyForms(t, page)
		})
	}
}

// TestBatchInvoiceCompanySetupActionMatchesRole links incomplete company data to
// its setup for administrators only; other roles get an explanation.
func TestBatchInvoiceCompanySetupActionMatchesRole(t *testing.T) {
	t.Parallel()
	for _, role := range []string{models.RoleViewer, models.RoleEditor, models.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			page := mainContent(t, execPage(t, "batch_invoice", map[string]any{
				"User": &models.User{Role: role, IsAdmin: role == models.RoleAdmin},
				"Year": models.BillingYear{ID: 1, Year: 2026}, "CompanyOK": false,
			}))
			if strings.Contains(page, `href="/admin/company"`) != (role == models.RoleAdmin) {
				t.Error("company setup link must only be offered to administrators")
			}
			if role != models.RoleAdmin && !strings.Contains(page, "von einem Administrator ergänzt") {
				t.Error("non-administrator lacks actionable explanation")
			}
			if role == models.RoleViewer && !strings.Contains(page, "data-readonly-notice") {
				t.Error("incomplete company must not hide the read-only explanation")
			}
		})
	}
}

// TestTemplateFixtureDefaultDoesNotOverrideExplicitViewer checks that execPage
// adds its default writer without mutating caller data and never replaces an
// explicit viewer.
func TestTemplateFixtureDefaultDoesNotOverrideExplicitViewer(t *testing.T) {
	t.Parallel()
	implicit := map[string]any{"Persons": []models.Person{}}
	writer := mainContent(t, execPage(t, "persons", implicit))
	if _, mutated := implicit["User"]; mutated {
		t.Error("default fixture user mutated caller data")
	}
	if !strings.Contains(writer, `action="/personen"`) {
		t.Error("historical authenticated fixture lost its default writer")
	}
	data := map[string]any{"User": &models.User{Role: models.RoleViewer}, "Persons": []models.Person{}}
	page := mainContent(t, execPage(t, "persons", data))
	if strings.Contains(page, `action="/personen"`) {
		t.Error("explicit viewer was replaced by fixture writer")
	}
}

// TestSharedInvoiceTypedNilUserRemainsPublic treats a typed-nil user on a shared
// invoice as anonymous: public output only, no account controls or notice.
func TestSharedInvoiceTypedNilUserRemainsPublic(t *testing.T) {
	t.Parallel()
	data := invoicePolishData()
	data["User"] = (*models.User)(nil)
	data["Shared"], data["PortalFeedbackEnabled"], data["PortalToken"] = true, true, "test-token"
	page := mainContent(t, execPage(t, "invoice_document", data))
	for _, want := range []string{`/s/portal/test-token/invoice.pdf`, `action="/s/portal/test-token/feedback"`, "178,20"} {
		if !strings.Contains(page, want) {
			t.Errorf("anonymous document lost public output %q", want)
		}
	}
	for _, forbidden := range []string{"Zahlung verbuchen", "Link erzeugen", "Rechnung stornieren", `data-readonly-notice`} {
		if strings.Contains(page, forbidden) {
			t.Errorf("anonymous document exposed account-only control %q", forbidden)
		}
	}
}

// TestReadOnlyNoticeHandlesAnonymousUser renders the read-only notice for a
// typed-nil user without failing.
func TestReadOnlyNoticeHandlesAnonymousUser(t *testing.T) {
	t.Parallel()
	pages, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := pages["persons"].ExecuteTemplate(&output, "readonly-notice", map[string]any{
		"User": (*models.User)(nil),
	}); err != nil {
		t.Fatalf("anonymous readonly notice: %v", err)
	}
	if !strings.Contains(output.String(), "data-readonly-notice") {
		t.Error("anonymous user lost the read-only explanation")
	}
}

// assertReadOnlyForms inspects rendered HTML, including disabled fieldset
// inheritance. Personal account preferences remain intentionally writable.
func assertReadOnlyForms(t *testing.T, source string) {
	t.Helper()
	checkReadOnlyForms(source, t.Errorf)
}

var personalNotificationForm = regexp.MustCompile(`^/notifications/(read-all|[1-9][0-9]*/(open|dismiss))$`)

func checkReadOnlyForms(source string, report func(string, ...any)) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		report("parse rendered HTML: %v", err)
		return
	}
	attr := func(n *html.Node, key string) (string, bool) {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val, true
			}
		}
		return "", false
	}
	var walk func(*html.Node, string, bool)
	walk = func(n *html.Node, form string, disabled bool) {
		if n.Type == html.ElementNode {
			if n.Data == "form" {
				if method, _ := attr(n, "method"); strings.EqualFold(method, "post") {
					form, _ = attr(n, "action")
					if form == "" {
						form = "(current page)"
					}
				}
			}
			_, ownDisabled := attr(n, "disabled")
			disabled = disabled || ownDisabled
			if form != "" && !strings.HasPrefix(form, "/account/") && !personalNotificationForm.MatchString(form) && !disabled {
				switch n.Data {
				case "input", "select", "textarea", "button":
					kind, _ := attr(n, "type")
					if kind != "hidden" && kind != "button" {
						report("read-only page exposes enabled <%s> in POST %s", n.Data, form)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, form, disabled)
		}
	}
	walk(doc, "", false)
}

func TestReadOnlyFormInspectionIncludesActionlessPosts(t *testing.T) {
	for _, tc := range []struct {
		source   string
		problems int
	}{
		{`<form method="post"><button>Submit</button></form>`, 1},
		{`<form method="post" action=""><input name="amount"></form>`, 1},
		{`<form method="get"><button>Search</button></form>`, 0},
		{`<form method="post"><fieldset disabled><button>Submit</button></fieldset></form>`, 0},
		{`<form method="post" action="/account/password"><button>Save</button></form>`, 0},
		{`<form method="post" action="/notifications/7/dismiss"><button>Dismiss</button></form>`, 0},
		{`<form method="post" action="/notifications/7/delete"><button>Delete</button></form>`, 1},
	} {
		var problems []string
		checkReadOnlyForms(tc.source, func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) })
		if len(problems) != tc.problems {
			t.Errorf("%s: got %v, want %d problems", tc.source, problems, tc.problems)
		}
	}
}
