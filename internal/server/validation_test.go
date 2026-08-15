package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLenError(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value string
		max   int
		want  string
	}{
		{"under limit", "Name", "abc", maxNameLen, ""},
		{"at limit", "Name", strings.Repeat("a", 100), maxNameLen, ""},
		{"one over limit", "Name", strings.Repeat("a", 101), maxNameLen, "Name darf höchstens 100 Zeichen lang sein."},
		{"note at limit", "Notiz", strings.Repeat("x", 500), maxNoteLen, ""},
		{"note one over limit", "Notiz", strings.Repeat("x", 501), maxNoteLen, "Notiz darf höchstens 500 Zeichen lang sein."},
		// Counts code points, not bytes: 100 × "ä" is 200 bytes but 100 runes.
		{"multibyte within limit", "Name", strings.Repeat("ä", 100), maxNameLen, ""},
		{"multibyte over limit", "Name", strings.Repeat("ä", 101), maxNameLen, "Name darf höchstens 100 Zeichen lang sein."},
		{"empty value", "Beschreibung", "", maxNoteLen, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lenError(c.field, c.value, c.max); got != c.want {
				t.Errorf("lenError(%q, runes=%d, max=%d) = %q, want %q",
					c.field, utf8.RuneCountInString(c.value), c.max, got, c.want)
			}
		})
	}
}

func TestSanitizeQueryParam(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"short query", "login", "login"},
		{"exact limit", strings.Repeat("a", 100), strings.Repeat("a", 100)},
		{"over limit truncated", strings.Repeat("a", 150), strings.Repeat("a", 100)},
		{"multibyte over limit", strings.Repeat("ä", 120), strings.Repeat("ä", 100)},
		{"whitespace trimmed", "   test   ", "test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeQueryParam(c.input)
			if got != c.want {
				t.Errorf("sanitizeQueryParam(%q) = %q (runes %d), want %q (runes %d)",
					c.input, got, utf8.RuneCountInString(got), c.want, utf8.RuneCountInString(c.want))
			}
		})
	}
}
