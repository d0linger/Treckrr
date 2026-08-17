package mail

import (
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
)

// TestSendRejectsHeaderInjection: a To address with CR/LF is rejected before any
// SMTP dialog, closing the header-injection vector.
func TestSendRejectsHeaderInjection(t *testing.T) {
	cfg := &config.Config{SMTPHost: "localhost", SMTPFrom: "mr@example.at"}
	err := Send(cfg, "victim@x.com\r\nBcc: leak@evil.com", "s", "b", nil)
	if err == nil || !strings.Contains(err.Error(), "ungültige") {
		t.Fatalf("expected rejection of CRLF address, got %v", err)
	}
}
