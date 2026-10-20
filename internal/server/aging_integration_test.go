//go:build integration

package server

import (
	"strings"
	"testing"
)

// TestDashboardAgingIntegration issues an invoice with an open remainder and
// expects the dashboard to show the aging card with a drawn segment for it.
func TestDashboardAgingIntegration(t *testing.T) {
	e := newItEnv(t)
	issuePaymentDeliveryInvoice(t, e, "100", "-44")
	page := e.get("/?year=" + itoa64(e.yearID64))
	for _, want := range []string{"Offene Rechnungsbeträge nach Fälligkeit", "noch nicht fällig", `class="aging__seg aging__seg--`, `href="/mahnwesen"`} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
}
