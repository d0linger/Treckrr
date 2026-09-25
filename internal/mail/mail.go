// Package mail sends a Beleg/Rechnung by e-mail with a PDF attachment via SMTP.
// Uses only net/smtp (STARTTLS + AUTH), no third-party client.
package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/config"
)

// Attachment is a file to attach (filename + bytes, content-type).
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// AmbiguousDeliveryError means the SMTP DATA phase started but Treckrr could
// not determine whether the server accepted the complete message. Retrying an
// ambiguous delivery automatically can send a duplicate.
type AmbiguousDeliveryError struct {
	err error
}

// Error returns the underlying transport failure.
func (e *AmbiguousDeliveryError) Error() string { return e.err.Error() }

// Unwrap exposes the underlying transport failure for errors.Is/As.
func (e *AmbiguousDeliveryError) Unwrap() error { return e.err }

// IsAmbiguous reports whether delivery may have succeeded despite the error.
func IsAmbiguous(err error) bool {
	var target *AmbiguousDeliveryError
	return errors.As(err, &target)
}

// Ambiguous wraps a transport error when delivery may already have occurred.
// It is primarily useful for injected senders and tests; Send applies it at the
// SMTP DATA boundary itself.
func Ambiguous(err error) error {
	if err == nil {
		return nil
	}
	return &AmbiguousDeliveryError{err: err}
}

// StableMessageID returns the RFC 5322 Message-ID for one logical delivery.
// It deliberately excludes timestamps and MIME boundaries so every retry of
// the same content carries the same identity for downstream deduplication.
func StableMessageID(from, to, subject, body string, atts []Attachment) string {
	h := sha256.New()
	for _, value := range []string{canonicalAddress(to), subject, body} {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	for _, att := range atts {
		_, _ = h.Write([]byte(att.Filename))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(att.ContentType))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(att.Data)
		_, _ = h.Write([]byte{0})
	}
	domain := "treckrr.invalid"
	if parsed, err := netmail.ParseAddress(strings.TrimSpace(from)); err == nil {
		if _, candidate, ok := strings.Cut(parsed.Address, "@"); ok && candidate != "" {
			domain = strings.ToLower(candidate)
		}
	}
	return fmt.Sprintf("<treckrr.%x@%s>", h.Sum(nil), domain)
}

// canonicalAddress normalizes a mailbox for stable delivery identity hashing.
func canonicalAddress(value string) string {
	parsed, err := netmail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(parsed.Address)
}

// Send delivers a plain-text mail with optional attachments to one recipient. It
// dials cfg.SMTPHost:SMTPPort, upgrades via STARTTLS when configured, authenticates
// (PLAIN) if a user is set, and sends. Errors are returned to the caller to surface.
func Send(ctx context.Context, cfg *config.Config, to, subject, body string, atts []Attachment) error {
	return SendWithMessageID(ctx, cfg, to, subject, body, atts,
		StableMessageID(cfg.SMTPFrom, to, subject, body, atts))
}

// SendWithMessageID delivers a message with a caller-persisted identity so a
// later retry remains recognizable even if SMTP configuration changes.
func SendWithMessageID(ctx context.Context, cfg *config.Config, to, subject, body string, atts []Attachment, messageID string) error {
	if !cfg.MailEnabled() {
		return fmt.Errorf("e-mail ist nicht konfiguriert")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || strings.ContainsAny(messageID, "\r\n") ||
		!strings.HasPrefix(messageID, "<") || !strings.HasSuffix(messageID, ">") {
		return fmt.Errorf("ungültige Message-ID")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("kein Empfänger")
	}
	// Reject anything that isn't a single, well-formed address — in particular a
	// value with embedded CR/LF, which would otherwise inject extra SMTP headers
	// (Bcc/spoofing) via the To line. Use the PARSED address downstream, not the raw
	// input: a comment-form value like "x@y (junk\r\nBcc: …)" parses OK but keeps the
	// CRLF in its raw form — feeding that to the To header / RCPT would still inject.
	parsed, err := netmail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("ungültige Empfängeradresse")
	}
	to = parsed.Address
	// The From may be configured with a display name ("MR <mr@x.at>") — fine for the
	// From: header, but the SMTP envelope (MAIL FROM) needs the bare address, or the
	// server rejects it. Parse once: raw string for the header, .Address for the envelope.
	fromHeader := strings.TrimSpace(cfg.SMTPFrom)
	fromEnvelope := fromHeader
	if pf, perr := netmail.ParseAddress(fromHeader); perr == nil {
		fromEnvelope = pf.Address
	}
	msg := buildMessageWithID(fromHeader, to, subject, body, atts, messageID)

	addr := cfg.SMTPHost + ":" + cfg.SMTPPort
	// Dial with an explicit timeout and set a deadline covering the whole exchange
	// (greeting, EHLO, STARTTLS, AUTH, delivery), so an unresponsive server can't
	// hang the request goroutine indefinitely.
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("SMTP-Verbindung: %w", err)
	}
	// The whole exchange must finish inside the HTTP server's 30 s WriteTimeout,
	// or the response is cut off while the mail may still go out and the user
	// sees an error for a delivery that happened. 10 s dial + 18 s dialog leaves
	// slack for rendering the redirect.
	_ = conn.SetDeadline(time.Now().Add(18 * time.Second))
	// And a canceled request (or a shutdown) aborts the dialog immediately
	// instead of letting a doomed exchange run to its deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP-Verbindung: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("treckrr"); err != nil {
		return err
	}
	if cfg.SMTPStartTLS {
		// STARTTLS is required when configured: if the server does not advertise it,
		// refuse rather than fall through and send credentials + PII in cleartext.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP-Server bietet kein STARTTLS an")
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if strings.TrimSpace(cfg.SMTPUser) != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return fmt.Errorf("SMTP-Auth: %w", err)
		}
	}
	if err := c.Mail(fromEnvelope); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		_ = wc.Close()
		return fmt.Errorf("SMTP-Datenübertragung: %w", err)
	}
	if err := wc.Close(); err != nil {
		// A structured SMTP reply is authoritative: 4xx/5xx means the server
		// rejected the message. EOF, timeout, or another transport failure after
		// the terminating dot leaves acceptance unknowable.
		var reply *textproto.Error
		if errors.As(err, &reply) {
			return err
		}
		return &AmbiguousDeliveryError{err: fmt.Errorf("SMTP-Bestätigung unklar: %w", err)}
	}
	// DATA was accepted. A failed connection shutdown must not ask the caller
	// to retry a message the SMTP server has already queued.
	_ = c.Quit()
	return nil
}

// buildMessage assembles a MIME multipart/mixed message (text + attachments).
func buildMessage(from, to, subject, body string, atts []Attachment) []byte {
	return buildMessageWithID(from, to, subject, body, atts,
		StableMessageID(from, to, subject, body, atts))
}

// buildMessageWithID assembles a MIME message with a retry-stable Message-ID.
func buildMessageWithID(from, to, subject, body string, atts []Attachment, messageID string) []byte {
	var b bytes.Buffer
	boundary := fmt.Sprintf("treckrr-%d", time.Now().UnixNano())
	enc := mime.QEncoding.Encode

	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", enc("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", messageID)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	// text part
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	writeBase64(&b, []byte(body))

	for _, a := range atts {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
		fmt.Fprintf(&b, "Content-Type: %s\r\n", ct)
		b.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=%q\r\n\r\n", a.Filename)
		writeBase64(&b, a.Data)
	}
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.Bytes()
}

// writeBase64 writes data base64-encoded in 76-char lines (RFC 2045).
func writeBase64(b *bytes.Buffer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
}
