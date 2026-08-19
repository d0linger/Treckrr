package server

import "testing"

func TestMaskIBAN(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"blank stays blank", "", ""},
		{"whitespace only stays blank", "   ", ""},
		{"full IBAN keeps last four", "AT611904300234573201", "…3201"},
		{"spaced IBAN keeps last four (trimmed)", "  AT61 1904 3002 3457 3201  ", "…3201"},
		{"short value dropped to marker", "3201", "••••"},
		{"exactly five keeps last four", "12345", "…2345"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maskIBAN(c.in); got != c.want {
				t.Errorf("maskIBAN(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestIBANChangeMarker(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"unchanged → empty", "AT611904300234573201", "AT611904300234573201", ""},
		{"only leading/trailing whitespace differs → empty", "  AT613201  ", "AT613201", ""},
		{"both blank → empty", "", "", ""},
		{"set from blank → masked new", "", "AT611904300234573201", "IBAN: — → …3201"},
		{"cleared → masked old to dash", "AT611904300234573201", "", "IBAN: …3201 → —"},
		{"different tails → masked both", "AT611904300234573201", "AT330000000000009876", "IBAN: …3201 → …9876"},
		// The regression case: a real change whose masked tails collide must NOT be
		// swallowed — it gets the explicit "geändert" marker instead.
		{"same last four, different account → explicit changed marker", "AT611904300234573201", "DE8937040044053201", "IBAN: geändert (…3201)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ibanChangeMarker(c.old, c.new); got != c.want {
				t.Errorf("ibanChangeMarker(%q, %q) = %q, want %q", c.old, c.new, got, c.want)
			}
		})
	}
}
