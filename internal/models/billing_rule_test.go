package models

import "testing"

// TestBilledHours pins the basis' billing rule: hours round UP to the increment,
// the minimum floors a short job, exact multiples stay put, and a basis without
// a rule bills the typed hours unchanged.
func TestBilledHours(t *testing.T) {
	cases := []struct {
		name, increment, minimum, hours, want string
	}{
		{"no rule", "0", "0", "2.1", "2.1"},
		{"quarter rounds up", "0.25", "0", "2.1", "2.25"},
		{"exact multiple stays", "0.25", "0", "2.25", "2.25"},
		{"tenth no float drift", "0.1", "0", "1.1", "1.1"},
		{"half hour", "0.5", "0", "0.01", "0.5"},
		{"minimum floors", "0", "1", "0.4", "1"},
		{"minimum above rounding", "0.25", "1", "0.3", "1"},
		{"above minimum rounds", "0.25", "1", "1.05", "1.25"},
	}
	for _, c := range cases {
		b := PriceBase{BillingIncrementH: dec(c.increment), MinimumH: dec(c.minimum)}
		if got := b.BilledHours(dec(c.hours)); !got.Equal(dec(c.want)) {
			t.Errorf("%s: BilledHours(%s) = %s, want %s", c.name, c.hours, got, c.want)
		}
		if want := c.increment != "0" || c.minimum != "0"; b.HasBillingRule() != want {
			t.Errorf("%s: HasBillingRule = %v, want %v", c.name, b.HasBillingRule(), want)
		}
	}
}
