package store

import (
	"testing"
	"time"
)

// TestAdvanceDateMonthly ensures monthly advance never skips a month and clamps to
// month-end (the Jan-31 → March-3 overflow bug is gone).
func TestAdvanceDateMonthly(t *testing.T) {
	d := func(s string) time.Time { t2, _ := time.Parse("2006-01-02", s); return t2 }
	cases := []struct{ in, want string }{
		{"2026-01-31", "2026-02-28"}, // was 2026-03-03 (skipped Feb) before the fix
		{"2026-02-28", "2026-03-28"},
		{"2026-01-15", "2026-02-15"},
		{"2026-12-31", "2027-01-31"},
		{"2028-01-31", "2028-02-29"}, // leap year
	}
	for _, c := range cases {
		if got := advanceDate(d(c.in), "monthly").Format("2006-01-02"); got != c.want {
			t.Errorf("advanceDate(%s, monthly) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := advanceDate(d("2026-03-02"), "weekly").Format("2006-01-02"); got != "2026-03-09" {
		t.Errorf("weekly advance wrong: %s", got)
	}
}
