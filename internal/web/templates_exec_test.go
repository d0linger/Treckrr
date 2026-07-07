package web

import (
	"bytes"
	"testing"

	"github.com/shopspring/decimal"
)

// execPage renders a page's full "layout" with the given data and fails on any
// template execution error. This guards against the class of bug where a
// template compared a decimal.Decimal against a float literal (gt/lt), which
// parses fine but errors at render time — producing a 500 in production.
func execPage(t *testing.T, page string, data map[string]any) {
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
}

func TestStatsPageRendersWithPreviousYear(t *testing.T) {
	d := decimal.NewFromFloat
	rows := []map[string]any{{"Label": "Musterhof", "Hours": d(2.17), "Cost": d(209.88)}}
	// Open year (nothing paid) that HAS a previous year — the exact case that
	// used to 500 because the comparison block compared decimals to floats.
	execPage(t, "stats", map[string]any{
		"Title":      "Statistik",
		"Year":       map[string]any{"Year": 2026, "ID": int64(3)},
		"TotalCost":  d(209.88),
		"TotalHours": d(2.17),
		"PaidCost":   d(0),
		"OpenCost":   d(209.88),
		"Completed":  false,
		"ByNeighbor": rows, "ByNeighborMax": d(209.88),
		"ByTask": rows, "ByTaskMax": d(209.88),
		"ByTractor": rows, "ByTractorMax": d(209.88),
		"PrevYear": 2025, "PrevCost": d(150), "PrevHours": d(1.5),
		"DiffCost": d(59.88), "DiffUp": true, "DiffDown": false,
		"DiffPct": d(39.92), "DiffPctUp": true,
	})
}

func TestStatsAllPageRenders(t *testing.T) {
	d := decimal.NewFromFloat
	execPage(t, "stats_all", map[string]any{
		"Title": "Statistik – Alle Jahre",
		"Stats": []map[string]any{
			{"Year": 2026, "YearID": int64(3), "Cost": d(209.88), "Hours": d(2.17), "PaidCost": d(0), "OpenCost": d(209.88), "Completed": false},
			{"Year": 2025, "YearID": int64(2), "Cost": d(150), "Hours": d(1.5), "PaidCost": d(150), "OpenCost": d(0), "Completed": true},
		},
		"Revenue":    []map[string]any{{"Label": "2026", "Hours": d(2.17), "Cost": d(209.88)}, {"Label": "2025", "Hours": d(1.5), "Cost": d(150)}},
		"RevenueMax": d(209.88),
		"GrandCost":  d(359.88), "GrandHours": d(3.67), "GrandPaid": d(150), "GrandOpen": d(209.88),
	})
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
