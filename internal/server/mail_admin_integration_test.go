package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/store"
)

// TestMailAdminCenterIntegration exercises the real admin routes, status strip,
// CSRF-protected retry action, and payload-free detail view.
func TestMailAdminCenterIntegration(t *testing.T) {
	e := newItEnv(t)
	m, _, err := e.st.CreateMailIntent(e.ctx, store.OutboxMail{
		Kind: "beleg", NeighborID: e.neighborID, BillingYearID: e.yearID64,
		Recipient: "mail-center@example.invalid", Subject: "Mail-Center Marker",
		Body: "secret body must not render", DeliveryKey: "it:server-mail-center:" + e.uname,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `
		UPDATE mail_outbox SET status='failed', attempts=6, terminal_at=now(), last_error='SMTP marker'
		 WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}

	list := e.get("/admin/mail?status=failed")
	for _, want := range []string{"Mailausgang", "Mail-Center Marker", "Fehlgeschlagen", "Unklar / angehalten"} {
		if !strings.Contains(list, want) {
			t.Errorf("mail list missing %q", want)
		}
	}
	detailPath := fmt.Sprintf("/admin/mail/%d", m.ID)
	detail := e.get(detailPath)
	if !strings.Contains(detail, "SMTP marker") || strings.Contains(detail, "secret body must not render") {
		t.Fatalf("detail did not keep payload private")
	}
	body := e.post(detailPath+"/retry", url.Values{})
	if !strings.Contains(body, "erneut eingeplant") {
		t.Fatalf("retry confirmation missing")
	}
	var status string
	var attempts int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT status, attempts FROM mail_outbox WHERE id=$1`, m.ID).
		Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != store.MailStatusPending || attempts != 0 {
		t.Fatalf("retry state = %s/%d, want pending/0", status, attempts)
	}
}
