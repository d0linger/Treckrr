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
				"Years":     []models.BillingYear{{ID: tc.yearID, Year: 2026}},
				"Completed": tc.completed,
				"GrandCost": decimal.Zero, "GrandHours": decimal.Zero,
				"PaidCost": decimal.Zero, "OpenCost": decimal.Zero,
			})
			_, main, found := strings.Cut(page, `<main class="main"`)
			if !found {
				t.Fatal("dashboard has no main landmark")
			}
			main, _, _ = strings.Cut(main, "</main>")
			want := fmt.Sprintf(`href="/buchungen?year=%d"`, tc.yearID)
			if count := strings.Count(main, want); count != 1 {
				t.Errorf("visible dashboard booking links = %d, want 1 with selected year", count)
			}
			for _, destination := range []string{"/export/year/", "/years"} {
				if !strings.Contains(main, `href="`+destination) {
					t.Errorf("existing dashboard destination %q was removed", destination)
				}
			}
			for _, destination := range []string{fmt.Sprintf("/stats?year=%d", tc.yearID), "/stats/all"} {
				if !strings.Contains(page, `href="`+destination+`"`) {
					t.Errorf("direct yearbar destination %q is missing", destination)
				}
				if strings.Contains(main, `href="`+destination+`"`) {
					t.Errorf("direct yearbar destination %q is still duplicated in the dashboard dropdown", destination)
				}
			}
			if !strings.Contains(main, `<details class="disclosure disclosure--plain page-more">`) ||
				!strings.Contains(main, "Weitere Aktionen") {
				t.Error("secondary dashboard destinations are not grouped under the reporting disclosure")
			}
		})
	}
}

// TestDashboardCoreWorkflow keeps one next action and the direct booking entry
// visible without duplicating the workflow as a second navigation system.
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
	if strings.Contains(page, "workpath") {
		t.Error("dashboard still duplicates the main navigation as a workflow stepper")
	}
	if count := strings.Count(page, `class="next-action`); count != 1 {
		t.Errorf("next actions = %d, want 1", count)
	}
	for _, want := range []string{"year-overview", "Abschluss prüfen", "Weitere Aktionen"} {
		if !strings.Contains(page, want) {
			t.Errorf("simplified dashboard missing %q", want)
		}
	}
	if !strings.Contains(page, `href="/neighbors/9?year=7&amp;view=booking">Buchen</a>`) {
		t.Error("open-year neighbor has no direct booking entry")
	}
	if strings.Contains(page, "favorite-toggle") {
		t.Error("dashboard still exposes an unnecessary neighbor favorite control")
	}
}

// TestNeighborAccountNavigation separates the long account into focused views
// without removing any booking, ledger, or payment destination.
func TestNeighborAccountNavigation(t *testing.T) {
	t.Parallel()
	base := map[string]any{
		"Title": "Demo-Hof Leitner",
		"Year": models.BillingYear{
			ID: 7, Year: 2026, Base: &models.PriceBase{ID: 2, Year: 2026},
		},
		"Base":              models.PriceBase{ID: 2, Year: 2026},
		"Neighbor":          models.Neighbor{ID: 9, Name: "Demo-Hof Leitner"},
		"BookingCount":      4,
		"TotalCost":         decimal.NewFromInt(120),
		"TotalHours":        decimal.NewFromInt(3),
		"Saldo":             decimal.NewFromInt(120),
		"LedgerSum":         decimal.Zero,
		"PaidSum":           decimal.Zero,
		"Remaining":         decimal.NewFromInt(120),
		"CreditAmount":      decimal.Zero,
		"Today":             "2026-10-02",
		"Stale":             map[int64]bool{},
		"PhotoCounts":       map[int64]int{},
		"LedgerPhotoCounts": map[int64]int{},
		"PairLabel":         map[int64]string{},
		"LinkedFrom":        map[int64]int64{},
	}

	base["Section"] = "overview"
	overview := execPage(t, "neighbor", base)
	for _, label := range []string{"Übersicht", "Buchen", "Buchungen", "Zahlungen", "Beleg", "account-summary", "Offen"} {
		if !strings.Contains(overview, label) {
			t.Errorf("neighbor overview missing %q", label)
		}
	}
	if strings.Contains(overview, "data-unified-booking") || strings.Contains(overview, `id="zahlungen"`) {
		t.Error("neighbor overview still renders full booking or payment workflows")
	}
	if count := strings.Count(overview, `href="/neighbors/9/beleg?year=7"`); count != 1 {
		t.Errorf("direct receipt links = %d, want one navigation entry", count)
	}
	for _, tc := range []struct {
		section string
		want    string
		avoid   string
	}{
		{section: "booking", want: "data-unified-booking", avoid: `id="zahlungen"`},
		{section: "bookings", want: `id="leistungen"`, avoid: "data-unified-booking"},
		{section: "payments", want: `id="zahlungen"`, avoid: "data-unified-booking"},
	} {
		base["Section"] = tc.section
		page := execPage(t, "neighbor", base)
		if !strings.Contains(page, tc.want) || strings.Contains(page, tc.avoid) {
			t.Errorf("neighbor section %q is not isolated", tc.section)
		}
	}

	base["Completed"] = true
	base["Section"] = "overview"
	completed := execPage(t, "neighbor", base)
	navStart := strings.Index(completed, `<nav class="section-tabs"`)
	if navStart < 0 {
		t.Fatal("completed neighbor page has no account navigation")
	}
	navEnd := strings.Index(completed[navStart:], `</nav>`)
	if navEnd < 0 {
		t.Fatal("completed neighbor navigation is not closed")
	}
	nav := completed[navStart : navStart+navEnd]
	if strings.Contains(nav, `view=booking"`) || strings.Contains(nav, ">Buchen<") {
		t.Error("completed neighbor navigation exposes the booking entry")
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
			for _, label := range []string{"Weitere Funktionen", "Konto &amp; Verwaltung"} {
				if !strings.Contains(page, label) {
					t.Errorf("drawer missing consolidated navigation label %q", label)
				}
			}
			drawerStart := strings.Index(page, `<aside class="drawer"`)
			if drawerStart < 0 {
				t.Fatal("application drawer is missing")
			}
			drawerEnd := strings.Index(page[drawerStart:], `</aside>`)
			if drawerEnd < 0 {
				t.Fatal("application drawer is not closed")
			}
			drawer := page[drawerStart : drawerStart+drawerEnd]
			for _, duplicate := range []string{`href="/"`, `href="/years"`, `href="/neighbors"`, `href="/bases"`} {
				if strings.Contains(drawer, duplicate) {
					t.Errorf("drawer still duplicates persistent navigation destination %q", duplicate)
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

// TestLeanApplicationShell keeps daily navigation visible while secondary
// controls stay focused: theme and year reports are direct, while search and
// administrative functions remain behind explicit disclosures.
func TestLeanApplicationShell(t *testing.T) {
	t.Parallel()
	page := execPage(t, "login", map[string]any{
		"User":   &models.User{Username: "editor", Role: models.RoleEditor},
		"Active": "dashboard",
		"Year":   &models.BillingYear{ID: 7, Year: 2026},
		"Years": []models.BillingYear{
			{ID: 7, Year: 2026},
			{ID: 6, Year: 2025},
		},
		"BasePath": "/",
	})

	headerEnd := strings.Index(page, "</header>")
	if headerEnd < 0 {
		t.Fatal("application shell has no header")
	}
	header := page[:headerEnd]
	for _, unwanted := range []string{"data-cmdk-open", `class="opsbar`} {
		if strings.Contains(header, unwanted) {
			t.Errorf("persistent header still exposes %q", unwanted)
		}
	}
	if !strings.Contains(header, "data-theme-toggle") || strings.Count(page, "data-theme-toggle") != 1 {
		t.Error("theme toggle must appear exactly once in the persistent top bar")
	}
	for _, want := range []string{
		"Weitere Funktionen",
		"Konto &amp; Verwaltung",
		"data-cmdk-open",
		"data-theme-toggle",
		"data-year-select",
		"yearbar__links",
		"yearbar__tools",
		"Statistik",
		"Jahresvergleich",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("lean shell missing %q", want)
		}
	}
	if strings.Contains(page, "Kernablauf") {
		t.Error("secondary drawer still duplicates the persistent core navigation")
	}
}

// TestBackupStatusStaysVisible keeps the reassuring healthy state in the app
// bar instead of showing the indicator only after backups need attention.
func TestBackupStatusStaysVisible(t *testing.T) {
	t.Parallel()

	for _, tone := range []string{"ok", "warn", "bad"} {
		t.Run(tone, func(t *testing.T) {
			t.Parallel()
			page := execPage(t, "login", map[string]any{
				"User": &models.User{Username: "editor", Role: models.RoleEditor},
				"BackupHealth": map[string]any{
					"Tone":     tone,
					"Title":    "Backup aktuell",
					"AgeLabel": "vor 2 Std.",
				},
			})

			if count := strings.Count(page, ` data-bk>`); count != 1 {
				t.Errorf("backup status indicators for tone %q = %d, want 1", tone, count)
			}
			if !strings.Contains(page, `class="bkdot bkdot--`+tone+`"`) {
				t.Errorf("backup status tone %q is not rendered in the app bar", tone)
			}
		})
	}
}
