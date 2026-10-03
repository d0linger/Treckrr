package server

import "testing"

// TestNeighborDetailSection keeps presentation routing on the four supported
// account views and sends stale or malformed links back to the overview.
func TestNeighborDetailSection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{want: "overview"},
		{raw: "overview", want: "overview"},
		{raw: "booking", want: "booking"},
		{raw: "bookings", want: "bookings"},
		{raw: "payments", want: "payments"},
		{raw: "../../admin", want: "overview"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := neighborDetailSection(tc.raw); got != tc.want {
				t.Errorf("neighborDetailSection(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
