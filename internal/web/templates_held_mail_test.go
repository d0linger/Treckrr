package web

import (
	"strings"
	"testing"
	"time"
)

// TestBackupPageListsHeldMail verifies mail parked by a restore is shown with
// release and discard actions, even when backups are not configured.
func TestBackupPageListsHeldMail(t *testing.T) {
	html := execPage(t, "backup", map[string]any{
		"Title":  "Backup",
		"Backup": map[string]any{"Enabled": false, "State": "none"},
		"HeldMail": []map[string]any{{
			"ID": int64(42), "Subject": "Rechnung R-2026-7", "Recipient": "n@example.invalid",
			"CreatedAt": time.Now(), "Attempts": 1, "LastError": "Nach Wiederherstellung angehalten (Status vorher: pending)",
		}},
	})
	for _, want := range []string{
		`id="held-mail"`, "Angehaltene E-Mails · 1", "Rechnung R-2026-7 → n@example.invalid",
		`action="/admin/backup/held-mail/42/release"`, `action="/admin/backup/held-mail/42/discard"`,
		"Freigeben", "Verwerfen",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("backup page lacks %q", want)
		}
	}
	if without := execPage(t, "backup", map[string]any{"Title": "Backup", "Backup": map[string]any{"State": "none"}}); strings.Contains(without, "held-mail") {
		t.Error("held-mail section rendered without held mail")
	}
}
