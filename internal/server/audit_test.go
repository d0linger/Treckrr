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
