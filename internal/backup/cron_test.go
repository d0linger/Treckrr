package backup

import (
	"testing"
	"time"
)

func TestDescribeCron(t *testing.T) {
	cases := map[string]string{
		"":             "Ausgeschaltet (kein Zeitplan).",
		"0 3 * * *":    "Täglich um 03:00 Uhr.",
		"30 4 * * *":   "Täglich um 04:30 Uhr.",
		"0 */6 * * *":  "Alle 6 Stunden (um Minute 0).",
		"*/30 * * * *": "Alle 30 Minuten.",
		"0 * * * *":    "Stündlich (zur vollen Stunde).",
		"0 3 * * 0":    "Jeden Sonntag um 03:00 Uhr.",
		"0 3 1 * *":    "Monatlich am 1. um 03:00 Uhr.",
		"not a cron":   "Ungültig: erwartet 5 Felder (Minute Stunde Tag Monat Wochentag).",
		"99 99 * * *":  "Ungültiger Cron-Ausdruck.",
	}
	for expr, want := range cases {
		if got := DescribeCron(expr); got != want {
			t.Errorf("DescribeCron(%q) = %q, want %q", expr, got, want)
		}
	}
}

func TestValidCron(t *testing.T) {
	for _, ok := range []string{"", "0 3 * * *", "*/15 * * * *"} {
		if !ValidCron(ok) {
			t.Errorf("ValidCron(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"0 3 * *", "99 * * * *", "abc"} {
		if ValidCron(bad) {
			t.Errorf("ValidCron(%q) = true, want false", bad)
		}
	}
}

func TestCronDue(t *testing.T) {
	at := func(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }
	now := at(2026, 8, 1, 12) // fixed reference: 2026-08-01 12:00
	cases := []struct {
		name string
		expr string
		last time.Time
		want bool
	}{
		{"never-run triggers immediately", "0 3 * * *", time.Time{}, true},
		{"already ran this window", "0 3 * * *", at(2026, 8, 1, 3), false},
		{"overdue since yesterday", "0 3 * * *", at(2026, 7, 31, 3), true},
		{"invalid expr never due", "nope", time.Time{}, false},
	}
	for _, c := range cases {
		if got := cronDue(c.expr, c.last, now); got != c.want {
			t.Errorf("%s: cronDue(%q, %v, %v) = %v, want %v", c.name, c.expr, c.last, now, got, c.want)
		}
	}
}
