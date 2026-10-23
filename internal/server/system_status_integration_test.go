//go:build integration

package server

import (
	"strings"
	"testing"
)

// TestSystemStatusIntegration renders the administrator's system page against
// a real database: every area appears, the schema level is current, and the
// configuration warnings this test server deserves (plain-HTTP cookies) show.
func TestSystemStatusIntegration(t *testing.T) {
	e := newItEnv(t)
	var latest string
	if err := e.pool.QueryRowContext(e.ctx, `SELECT max(name) FROM schema_migrations`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	page := e.get("/admin/system")
	for _, want := range []string{"Systemzustand", "Betrieb", "Datenbank", "Sicherung", "E-Mail", "Anmeldung",
		"PostgreSQL", "Migrationen", latest, "ohne HTTPS übertragen", "Zweiter Faktor",
		"Protokoll", "Prüfkette Geschäftsvorfälle", "Prüfkette Anmeldung und Betrieb"} {
		if !strings.Contains(page, want) {
			t.Errorf("system page lacks %q", want)
		}
	}
	if !strings.Contains(page, `href="/admin/system" aria-current="page"`) {
		t.Error("drawer does not mark the system page as current")
	}
}
