package server

import "testing"

// TestPriceSection limits presentation routing to known catalog editors.
func TestPriceSection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{want: "machines"},
		{raw: "fuel", want: "fuel"},
		{raw: "loads", want: "loads"},
		{raw: "tractors", want: "tractors"},
		{raw: "machines", want: "machines"},
		{raw: "unknown", want: "machines"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := priceSection(tc.raw); got != tc.want {
				t.Errorf("priceSection(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
