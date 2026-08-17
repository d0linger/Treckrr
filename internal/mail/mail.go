// Package mail sends a Beleg/Rechnung by e-mail with a PDF attachment via SMTP.
// Uses only net/smtp (STARTTLS + AUTH), no third-party client.
package mail

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
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

// Send delivers a plain-text mail with optional attachments to one recipient. It
// dials cfg.SMTPHost:SMTPPort, upgrades via STARTTLS when configured, authenticates
// (PLAIN) if a user is set, and sends. Errors are returned to the caller to surface.
func Send(cfg *config.Config, to, subject, body string, atts []Attachment) error {
	if !cfg.MailEnabled() {
		return fmt.Errorf("e-mail ist nicht konfiguriert")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("kein Empfänger")
	}
	msg := buildMessage(cfg.SMTPFrom, to, subject, body, atts)

	addr := cfg.SMTPHost + ":" + cfg.SMTPPort
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP-Verbindung: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("treckrr"); err != nil {
		return err
	}
	if cfg.SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("STARTTLS: %w", err)
			}
		}
	}
	if strings.TrimSpace(cfg.SMTPUser) != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return fmt.Errorf("SMTP-Auth: %w", err)
		}
	}
	if err := c.Mail(cfg.SMTPFrom); err != nil {
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
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage assembles a MIME multipart/mixed message (text + attachments).
func buildMessage(from, to, subject, body string, atts []Attachment) []byte {
	var b bytes.Buffer
	boundary := fmt.Sprintf("treckrr-%d", time.Now().UnixNano())
	enc := mime.QEncoding.Encode

	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", enc("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
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
