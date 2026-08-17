package mail

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildMessage(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 fake")
	msg := buildMessage("mr@example.at", "kunde@example.at", "Rechnung 2026-001",
		"Guten Tag,\nanbei die Rechnung.", []Attachment{
			{Filename: "Rechnung_2026-001.pdf", ContentType: "application/pdf", Data: pdfBytes},
		})
	s := string(msg)
	for _, want := range []string{
		"From: mr@example.at", "To: kunde@example.at",
		"Subject: Rechnung 2026-001",
		"MIME-Version: 1.0", "multipart/mixed; boundary=",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="Rechnung_2026-001.pdf"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q", want)
		}
	}
	// The PDF bytes must be present base64-encoded.
	if !strings.Contains(s, base64.StdEncoding.EncodeToString(pdfBytes)) {
		t.Error("attachment not base64-encoded in message")
	}
	// CRLF line endings throughout the header.
	if !bytes.Contains(msg, []byte("\r\n")) {
		t.Error("message must use CRLF")
	}
}
