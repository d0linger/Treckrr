package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestWriteCSVTextCellBoundaries checks that every text column of the booking
// CSV escapes formula prefixes, even after leading whitespace, without shifting
// cell boundaries or changing numeric cells.
func TestWriteCSVTextCellBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
	}{
		{name: "formula with pipe", input: "=1|2", want: "'=1|2"},
		{name: "formula with percent", input: "=30%", want: "'=30%"},
		{name: "positive formula with percent", input: "+30%", want: "'+30%"},
		{name: "negative formula with percent", input: "-30%", want: "'-30%"},
		{name: "at formula with pipe", input: "@SUM(1)|2", want: "'@SUM(1)|2"},
		{name: "formula after whitespace", input: " \t\r\n=1+1", want: "' \t\r\n=1+1"},
		{name: "embedded separators", input: "note;\"=1+1\n@SUM(1)", want: "note;\"=1+1\n@SUM(1)"},
		{name: "literal percent", input: "%1", want: "%1"},
		{name: "literal pipe", input: "|example", want: "|example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entry := models.Entry{
				NeighborID:    1,
				Date:          time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
				TaskLabel:     tc.input,
				TractorLabel:  tc.input,
				LoadLabel:     tc.input,
				MachineLabels: tc.input,
				Unit:          tc.input,
				Note:          tc.input,
				Quantity:      decimal.NewFromInt(2),
				UnitPrice:     decimal.New(125, -1),
				Cost:          decimal.NewFromInt(-25),
			}
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/export", nil)
			s := testServer()
			s.writeExport(rr, r, "test", entriesTable("Buchungen", []models.Entry{entry}, map[int64]string{1: tc.input}))

			reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(rr.Body.String(), "\uFEFF")))
			reader.Comma = ';'
			rows, err := reader.ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 || len(rows[1]) != 11 {
				t.Fatalf("CSV row/column boundaries changed: %v", rows)
			}
			// encoding/csv normalizes CRLF inside quoted fields when reading.
			want := strings.ReplaceAll(tc.want, "\r\n", "\n")
			for _, column := range []int{0, 2, 3, 4, 5, 6, 10} {
				if got := rows[1][column]; got != want {
					t.Errorf("text column %d = %q, want %q", column, got, want)
				}
			}
			if rows[1][7] != "2,00" || rows[1][8] != "12,50" || rows[1][9] != "-25,00" || rows[2][9] != "-25,00" {
				t.Fatalf("numeric cells or total changed: %v", rows)
			}
		})
	}
}

// TestCSVSafeLiteralPrefixes keeps text that starts with % or | unchanged, also
// after leading whitespace.
func TestCSVSafeLiteralPrefixes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input string
	}{
		{name: "percent", input: "%1"},
		{name: "percent before function", input: "%SUM(1)"},
		{name: "pipe", input: "|cmd"},
		{name: "percent after whitespace", input: " \t%1"},
		{name: "pipe after whitespace", input: "\r\n|cmd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := csvSafe(tc.input); got != tc.input {
				t.Fatalf("csvSafe(%q) = %q, want unchanged text", tc.input, got)
			}
		})
	}
}
