package server

import (
	"context"
	"log/slog"
	"strings"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/models"
)

// ---- E-Mail-Vorlagen und Kopie-Empfänger (Ausbaukarte 99) ------------------
//
// The same three-line German body was written out in three places (Beleg mail,
// single Mahnung, batch Mahnung). One composer instead, so the wording — and
// the operator's own signature — lives in exactly one spot.

// mailBody builds the outgoing plain-text body. The signature comes from the
// Betriebsdaten; empty falls back to the wording that was hard-coded before,
// so a farm that configures nothing sees no change. The sender fallback is
// derived HERE: it used to be a parameter every caller computed with the same
// four lines, and the fallback name lived in three files.
func mailBody(company models.Company, recipientName, intro string) string {
	sig := strings.TrimSpace(company.MailSignature)
	if sig == "" {
		from := strings.TrimSpace(company.Name)
		if from == "" {
			from = "Ihr Maschinenring"
		}
		sig = "Mit freundlichen Grüßen\n" + from
	}
	return "Guten Tag " + recipientName + ",\n\n" + intro + "\n\n" + sig
}

// sendMailCopy delivers a copy of an outgoing mail to the configured CC
// address. It is best-effort ON PURPOSE: the neighbor already has their
// document, and parking a failed copy for retry could deliver it twice to
// someone who did receive it. A failure is logged, not surfaced as an error on
// a send that succeeded.
func (s *Server) sendMailCopy(ctx context.Context, company models.Company, subject, body string, atts []mail.Attachment) {
	cc := strings.TrimSpace(company.MailCC)
	if cc == "" {
		return
	}
	if err := mail.Send(ctx, s.cfg, cc, "[Kopie] "+subject, body, atts); err != nil {
		slog.Warn("mail copy to the configured CC failed", "err", sanitizeLog(err.Error()))
	}
}
