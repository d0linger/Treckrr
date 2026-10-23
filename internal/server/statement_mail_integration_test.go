//go:build integration

package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestStatementEmailIntegration mails the account statement through the
// outbox: without an address nothing is queued, with one the PDF statement is
// queued once per day (the env's SMTP refuses, so the mail stays for retry),
// and a second send on the same day does not create a second mail.
func TestStatementEmailIntegration(t *testing.T) {
	e := newItEnv(t)
	path := fmt.Sprintf("/neighbors/%d/overview/email", e.neighborID)
	count := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM mail_outbox WHERE kind='statement' AND neighbor_id=$1`, e.neighborID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if page := e.post(path, url.Values{}); !strings.Contains(page, "keine E-Mail-Adresse hinterlegt") || count() != 0 {
		t.Fatal("a neighbor without address must not get a statement mail")
	}
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE neighbors SET email='kontoauszug@example.invalid' WHERE id=$1`, e.neighborID); err != nil {
		t.Fatal(err)
	}
	if overview := e.get(fmt.Sprintf("/neighbors/%d/overview", e.neighborID)); !strings.Contains(overview, `action="`+path+`"`) {
		t.Fatal("overview lacks the send button")
	}
	e.post(path, url.Values{})
	if count() != 1 {
		t.Fatalf("statement mails=%d, want 1", count())
	}
	var attName, attType, recipient string
	var attBytes int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT att_name, att_type, recipient, octet_length(att_data) FROM mail_outbox
		WHERE kind='statement' AND neighbor_id=$1`, e.neighborID).Scan(&attName, &attType, &recipient, &attBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(attName, "Kontoauszug_") || attType != "application/pdf" || recipient != "kontoauszug@example.invalid" || attBytes < 1000 {
		t.Fatalf("attachment %q %q to %q (%d bytes)", attName, attType, recipient, attBytes)
	}
	e.post(path, url.Values{})
	if count() != 1 {
		t.Fatalf("a second send on the same day created another mail: %d", count())
	}
}
