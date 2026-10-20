package server

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// TestBuildICS checks the iCalendar output: CRLF lines of at most 75 octets
// that never split a character, escaped text, all-day events, and a minimal
// mode without names or amounts.
func TestBuildICS(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	events := []store.CalendarEvent{{
		UID: "invoice-7", Day: day, Kind: "invoice", Neighbor: "Hof Grüber; Söhne, KG",
		Title: "Rechnung 2026-003 fällig", Amount: decimal.NullDecimal{Decimal: decimal.RequireFromString("1234.5"), Valid: true},
		Path: "/neighbors/1/beleg?year=2",
	}, {
		UID: "recurring-3", Day: day, Kind: "recurring", Neighbor: "Moser",
		Title: "Serie läuft · " + strings.Repeat("Ährenernte mit Ladewagen ", 6), Path: "/recurring",
	}}
	now := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	full := string(buildICS(events, false, "treckrr.example", "https://treckrr.example", now))
	if !strings.HasPrefix(full, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(full, "END:VCALENDAR\r\n") {
		t.Fatal("calendar not framed with CRLF lines")
	}
	for _, line := range strings.Split(strings.TrimSuffix(full, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Errorf("line longer than 75 octets: %q", line)
		}
		if !utf8.ValidString(line) {
			t.Errorf("folding split a character: %q", line)
		}
	}
	unfolded := strings.ReplaceAll(full, "\r\n ", "")
	for _, want := range []string{"UID:invoice-7@treckrr.example", "DTSTART;VALUE=DATE:20261103", "DTEND;VALUE=DATE:20261104",
		`SUMMARY:Rechnung 2026-003 fällig · Hof Grüber\; Söhne\, KG · 1.234\,50 €`, "URL:https://treckrr.example/neighbors/1/beleg?year=2",
		"DTSTAMP:20261020T083000Z", strings.Repeat("Ährenernte mit Ladewagen ", 6)} {
		if !strings.Contains(unfolded, want) {
			t.Errorf("calendar lacks %q", want)
		}
	}
	minimal := strings.ReplaceAll(string(buildICS(events, true, "treckrr.example", "https://treckrr.example", now)), "\r\n ", "")
	if strings.Contains(minimal, "Grüber") || strings.Contains(minimal, "1.234") || strings.Contains(minimal, "Moser") {
		t.Fatal("minimal calendar still names neighbors or amounts")
	}
	if !strings.Contains(minimal, "SUMMARY:Rechnung 2026-003 fällig\r\n") {
		t.Fatal("minimal calendar lost the event title")
	}
}
