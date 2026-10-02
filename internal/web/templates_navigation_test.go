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
			if !strings.Contains(main, `<details class="summary-actions">`) ||
				!strings.Contains(main, "Auswertungen &amp; Daten") {
				t.Error("secondary dashboard destinations are not grouped under the reporting disclosure")
			}
		})
	}
}

// TestDashboardCoreWorkflow keeps the daily path and its direct booking entry
// visible without promoting the secondary reporting modules again.
func TestDashboardCoreWorkflow(t *testing.T) {
	t.Parallel()
	page := execPage(t, "dashboard", map[string]any{
		"User": models.User{ID: 1, Username: "editor", Role: models.RoleEditor},
		"Year": models.BillingYear{
			ID: 7, Year: 2026, Base: &models.PriceBase{Year: 2025},
		},
		"BookingCount": 4,
		"GrandCost":    decimal.NewFromInt(120),
		"GrandHours":   decimal.NewFromInt(3),
		"PaidCost":     decimal.Zero,
		"OpenCost":     decimal.NewFromInt(120),
		"Summaries": []map[string]any{{
			"Neighbor":  models.Neighbor{ID: 9, Name: "Demo-Hof Leitner"},
			"Cost":      decimal.NewFromInt(120),
			"Hours":     decimal.NewFromInt(3),
			"Entries":   4,
			"Remaining": decimal.NewFromInt(120),
		}},
	})
	for _, label := range []string{"Grundlage", "Nachbarn", "Buchungen", "Abschluss", "Bezahlt"} {
		if !strings.Contains(page, ">"+label+"</strong>") {
			t.Errorf("core workflow missing %q", label)
		}
	}
	if count := strings.Count(page, `<li class="workpath__step`); count != 5 {
		t.Errorf("workflow steps = %d, want 5", count)
	}
	if !strings.Contains(page, `href="/neighbors/9?year=7#neue-buchung">Buchen</a>`) {
		t.Error("open-year neighbor has no direct booking entry")
	}
	if !strings.Contains(page, `<small>4 erfasst</small>`) {
		t.Error("workflow does not expose the current booking count")
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
			for _, label := range []string{"Kernablauf", "Jahre &amp; Abschluss", "Nachbarn &amp; Buchungen", "Grundlagen", "Weitere Funktionen"} {
				if !strings.Contains(page, label) {
					t.Errorf("drawer missing consolidated navigation label %q", label)
				}
			}
			link := links[0]
			if !strings.Contains(link, `href="`+tc.href+`"`) || !strings.Contains(link, "Buchungen &amp; Filter</a>") {
				t.Errorf("booking link must retain year and consistent label: %s", link)
			}
			wantActive := tc.active == "entries"
			if strings.Contains(link, `aria-current="page"`) != wantActive || strings.Contains(link, "is-active") != wantActive {
				t.Errorf("booking active state does not match current page %q", tc.active)
			}
			advanced := regexp.MustCompile(`(?s)<details class="drawer__group"([^>]*)>.*?Buchungen &amp; Filter</a>`).FindStringSubmatch(page)
			if len(advanced) != 2 {
				t.Fatal("booking navigation is not inside the advanced-function disclosure")
			}
			if strings.Contains(advanced[1], "open") != wantActive {
				t.Errorf("advanced-function disclosure open state does not match current page %q", tc.active)
			}
		})
	}
}
