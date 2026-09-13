//go:build integration

package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestUnifiedBookingListIntegration exercises the real SQL/HTTP path with mixed
// outgoing work, incoming services, legacy postings, transfers and voided rows.
func TestUnifiedBookingListIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-09-13"},
		"hours": {"1"}, "unit": {"h"},
	})
	dec := decimal.RequireFromString
	for _, tc := range []struct {
		name, amount string
		booking      *models.LedgerBooking
		transfer     string
		voided       bool
	}{
		{name: "incoming equipment", amount: "-100", booking: &models.LedgerBooking{Version: 1, Kind: "equipment", TaskLabel: "Pflügen", PartnerLabel: "Traktor 50%_A", Unit: "h", Quantity: dec("2"), UnitPrice: dec("34.995"), PartnerPerson: "Franz CSV", PersonHours: dec("1.5"), PersonRate: dec("20.005")}},
		{name: "incoming labor", amount: "-30", booking: &models.LedgerBooking{Version: 1, Kind: "labor", TaskLabel: "Mithilfe", PartnerPerson: "Hans", Unit: "h", Quantity: dec("1.5"), UnitPrice: dec("20")}},
		{name: "outgoing cost", amount: "12.50", booking: &models.LedgerBooking{Version: 1, Kind: "fixed", TaskLabel: "=SUM(A1)", Unit: "Pauschale", Quantity: dec("1"), UnitPrice: dec("12.5")}},
		{name: "legacy debt", amount: "-7.50"},
		{name: "opening balance", amount: "8", transfer: "test-unified-transfer"},
		{name: "voided incoming", amount: "-99", voided: true, booking: &models.LedgerBooking{Version: 1, Kind: "fixed", TaskLabel: "Storniert", Unit: "Pauschale", Quantity: dec("1"), UnitPrice: dec("99")}},
	} {
		var booking any
		description := tc.name
		if tc.booking != nil {
			raw, err := json.Marshal(tc.booking)
			if err != nil {
				t.Fatal(err)
			}
			booking = string(raw)
			description = tc.booking.Summary()
		}
		if _, err := e.pool.ExecContext(e.ctx, `INSERT INTO neighbor_ledger
			(billing_year_id, neighbor_id, amount, description, posting_date, booking, transfer_id, voided)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, e.yearID64, e.neighborID, tc.amount, description,
			time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), booking, tc.transfer, tc.voided); err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
	}
	base := store.BookingFilter{EntryFilter: store.EntryFilter{YearID: e.yearID64, Limit: 50}}
	for _, tc := range []struct {
		name, query, sum string
		count            int
	}{
		{name: "all", count: 7, sum: "-71"},
		{name: "incoming", query: "direction=in", count: 4, sum: "-137.5"},
		{name: "outgoing", query: "direction=out", count: 3, sum: "66.5"},
		{name: "labor", query: "kind=labor", count: 1, sum: "-30"},
		{name: "equipment", query: "kind=equipment", count: 2, sum: "-54"},
		{name: "manual", query: "kind=manual", count: 1, sum: "-7.5"},
		{name: "transfer", query: "kind=transfer", count: 1, sum: "8"},
		{name: "literal_search", query: "task=50%25_A", count: 1, sum: "-100"},
		{name: "person_search", query: "task=Hans", count: 1, sum: "-30"},
		{name: "unit", query: "unit=Pauschale", count: 2, sum: "12.5"},
		{name: "only_voided", query: "voided=only", count: 1, sum: "0"},
		{name: "hide_voided", query: "voided=hide", count: 6, sum: "-71"},
		{name: "date_bound", query: "from=2026-09-14", count: 0, sum: "0"},
		{name: "unknown_neighbor", query: "neighbor_id=999999999", count: 0, sum: "0"},
		{name: "search_injection", query: "task=%27%20OR%20TRUE%3B--", count: 0, sum: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse("/buchungen?" + tc.query)
			if err != nil {
				t.Fatal(err)
			}
			f := base
			f.Direction, f.Kind = u.Query().Get("direction"), u.Query().Get("kind")
			f.Task, f.Unit, f.Voided = u.Query().Get("task"), u.Query().Get("unit"), u.Query().Get("voided")
			f.From = parseDay(u.Query().Get("from"))
			if u.Query().Get("neighbor_id") != "" {
				f.NeighborID = 999999999
			}
			rows, total, sum, err := e.st.FilterBookings(e.ctx, f)
			if err != nil || total != tc.count || len(rows) != tc.count || !sum.Equal(dec(tc.sum)) {
				t.Fatalf("rows=%d total=%d sum=%s err=%v; want %d/%s", len(rows), total, sum, err, tc.count, tc.sum)
			}
		})
	}
	// Every source participates in the same stable pagination, with no duplicate
	// or omitted rows even though the independent tables may reuse numeric IDs.
	for _, sort := range []string{"date", "cost", "neighbor"} {
		seen := map[string]bool{}
		f := base
		f.Limit, f.Sort = 2, sort
		for offset := 0; offset < 7; offset += 2 {
			f.Offset = offset
			rows, total, sum, err := e.st.FilterBookings(e.ctx, f)
			if err != nil || total != 7 || !sum.Equal(dec("-71")) {
				t.Fatalf("pagination: total=%d sum=%s err=%v", total, sum, err)
			}
			for _, row := range rows {
				key := fmt.Sprintf("%s:%d", row.Source, row.ID)
				if seen[key] {
					t.Fatalf("duplicate paginated row %s", key)
				}
				seen[key] = true
			}
		}
		if len(seen) != 7 {
			t.Errorf("%s paging returned %d distinct rows, want 7", sort, len(seen))
		}
	}
	f := base
	f.Limit, f.Offset = 1, 500
	if exported, err := e.st.ExportBookings(e.ctx, f); err != nil || len(exported) != 7 {
		t.Fatalf("export must ignore paging: rows=%d err=%v", len(exported), err)
	}
	units, err := e.st.BookingUnitsInYear(e.ctx, e.yearID64)
	if err != nil || strings.Join(units, ",") != "Pauschale,h" {
		t.Errorf("unified units = %v, err=%v", units, err)
	}
	listURL := "/buchungen?year=" + itoa64(e.yearID64)
	page := e.get(listURL)
	if !strings.Contains(page, "7 Buchung(en)") || strings.Count(page, `name="entry_id"`) != 1 || !strings.Contains(page, "Jahresübertrag") {
		t.Fatal("combined UI omitted rows, source labels, or exposed ledger bulk checkboxes")
	}
	incomingPage := e.get(listURL + "&direction=in")
	if strings.Contains(incomingPage, `name="entry_id"`) || strings.Contains(incomingPage, `name="action" value="delete"`) {
		t.Fatal("incoming-only view exposes outgoing bulk actions")
	}
	// CSV uses the same filters, neutralizes spreadsheet formulas, and exposes
	// source plus status so a reversed amount is never mistaken for own revenue.
	body := strings.TrimPrefix(e.get(listURL+"&export=csv&direction=in&voided=hide&page=9"), "\ufeff")
	reader := csv.NewReader(strings.NewReader(body))
	reader.Comma = ';'
	records, err := reader.ReadAll()
	if err != nil || len(records) != 5 {
		t.Fatalf("filtered CSV records=%d err=%v", len(records), err)
	}
	if records[len(records)-1][10] != "-137,50" {
		t.Errorf("CSV sum = %q, want -137,50", records[len(records)-1][10])
	}
	if len(records[0]) != 17 {
		t.Fatalf("CSV has %d columns, want 17 including exact helper fields", len(records[0]))
	}
	var foundHelper bool
	for _, record := range records[1 : len(records)-1] {
		if record[6] == "Pflügen" {
			foundHelper = true
			if record[9] != "34,995" || record[13] != "Traktor 50%_A" || record[14] != "Franz CSV" || record[15] != "1,5" || record[16] != "20,005" {
				t.Errorf("CSV lost independent helper precision: %#v", record)
			}
		}
	}
	if !foundHelper {
		t.Fatal("CSV omitted incoming equipment/helper row")
	}
	formulaCSV := e.get(listURL + "&export=csv&direction=out&kind=fixed")
	if !strings.Contains(formulaCSV, "'=SUM(A1)") {
		t.Fatal("unified CSV did not neutralize a formula-like task")
	}
	// More than the largest UI page limit must still export in full. Fixtures
	// are confined to this test's isolated billing-year/neighbor account.
	if _, err := e.pool.ExecContext(e.ctx, `INSERT INTO neighbor_ledger
		(billing_year_id, neighbor_id, amount, description, posting_date)
		SELECT $1,$2,1,'Export boundary ' || i,'2026-09-13'::date FROM generate_series(1,505) i`,
		e.yearID64, e.neighborID); err != nil {
		t.Fatalf("seed export boundary: %v", err)
	}
	exported, err := e.st.ExportBookings(e.ctx, f)
	if err != nil || len(exported) != 512 {
		t.Fatalf("export beyond 500 rows: got %d err=%v, want 512", len(exported), err)
	}
	lastPage := e.get(listURL + "&page=11")
	if !strings.Contains(lastPage, "512 Buchung(en)") || strings.Count(lastPage, `data-booking-source=`) != 12 {
		t.Fatal("mixed-source last page/count do not match all 512 rows")
	}
}
