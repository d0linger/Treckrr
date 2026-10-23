package web

import (
	"strings"
	"testing"
	"time"
)

// TestStatementShareEmptyState renders the public running statement with no
// open work and no account row: the page explains the empty state, keeps the
// confirmation form, offers no line choice and shows no app chrome.
func TestStatementShareEmptyState(t *testing.T) {
	t.Parallel()
	page := execPage(t, "statement_share", map[string]any{
		"User": nil, "Title": "Zwischenstand 2026", "StatementToken": "tok",
		"Statement": map[string]any{
			"Issuer": "Übungshof", "Recipient": "Hof <Gruber>", "Year": 2026,
			"AsOf": time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC), "Pending": nil, "Account": nil,
		},
	})
	for _, want := range []string{"Hof &lt;Gruber&gt;", "Übungshof · Stand 16.10.2026", "Derzeit sind keine Leistungen offen",
		`action="/s/konto/tok/feedback"`, "Stand bestätigen"} {
		if !strings.Contains(page, want) {
			t.Errorf("empty statement lacks %q", want)
		}
	}
	for _, forbidden := range []string{`name="line"`, "Abmelden", "kpigrid"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("empty statement shows %q", forbidden)
		}
	}
}
