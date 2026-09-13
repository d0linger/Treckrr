package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestDashboardBookingNavigation keeps the booking overview visible in both
// open and completed years without losing the selected billing-year context.
func TestDashboardBookingNavigation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		yearID    int64
		completed bool
	}{
		{name: "open_year", yearID: 7},
		{name: "completed_year", yearID: 42, completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := execPage(t, "dashboard", map[string]any{
				"User": models.User{ID: 1, Username: "reader", Role: models.RoleViewer},
				"Year": models.BillingYear{
					ID: tc.yearID, Year: 2026, Base: &models.PriceBase{Year: 2025},
				},
				"Completed": tc.completed,
				"GrandCost": decimal.Zero, "GrandHours": decimal.Zero,
				"PaidCost": decimal.Zero, "OpenCost": decimal.Zero,
			})
			_, main, found := strings.Cut(page, `<main class="main"`)
			if !found {
				t.Fatal("dashboard has no main landmark")
			}
			main, _, _ = strings.Cut(main, "</main>")
			want := fmt.Sprintf(`href="/buchungen?year=%d">Buchungen &amp; Filter</a>`, tc.yearID)
			if count := strings.Count(main, want); count != 1 {
				t.Errorf("visible dashboard booking links = %d, want 1 with selected year", count)
			}
			for _, destination := range []string{"/stats?year=", "/stats/all", "/export/year/", "/years"} {
				if !strings.Contains(main, `href="`+destination) {
					t.Errorf("existing dashboard destination %q was removed", destination)
				}
			}
		})
	}
}

// TestDrawerBookingNavigation preserves year context, active-page semantics,
// and the signed-in navigation boundary for the consistently named entry.
func TestDrawerBookingNavigation(t *testing.T) {
	t.Parallel()
	bookingLink := regexp.MustCompile(`(?s)<a class="drawer__item[^"]*" href="/buchungen[^"]*"[^>]*>.*?</a>`)
	for _, tc := range []struct {
		name   string
		user   *models.User
		year   *models.BillingYear
		active string
		href   string
	}{
		{name: "active_year", user: &models.User{Username: "reader"}, year: &models.BillingYear{ID: 7}, active: "entries", href: "/buchungen?year=7"},
		{name: "other_page", user: &models.User{Username: "reader"}, year: &models.BillingYear{ID: 42}, active: "dashboard", href: "/buchungen?year=42"},
		{name: "without_year", user: &models.User{Username: "reader"}, href: "/buchungen"},
		{name: "anonymous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page := execPage(t, "login", map[string]any{
				"User": tc.user, "Year": tc.year, "Active": tc.active,
			})
			links := bookingLink.FindAllString(page, -1)
			if tc.user == nil {
				if len(links) != 0 {
					t.Fatal("anonymous page exposes authenticated booking navigation")
				}
				return
			}
			if len(links) != 1 {
				t.Fatalf("drawer booking links = %d, want 1", len(links))
			}
			link := links[0]
			if !strings.Contains(link, `href="`+tc.href+`"`) || !strings.Contains(link, "Buchungen &amp; Filter</a>") {
				t.Errorf("booking link must retain year and consistent label: %s", link)
			}
			wantActive := tc.active == "entries"
			if strings.Contains(link, `aria-current="page"`) != wantActive || strings.Contains(link, "is-active") != wantActive {
				t.Errorf("booking active state does not match current page %q", tc.active)
			}
		})
	}
}
