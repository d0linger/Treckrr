package mail

import (
	"bufio"
	"net"
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

// TestSendRequiresStartTLS: when STARTTLS is configured but the server does not
// advertise the extension, Send must refuse rather than send in cleartext.
func TestSendRequiresStartTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// Minimal SMTP server that greets and answers EHLO without a STARTTLS line.
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
		for {
			line, rerr := br.ReadString('\n')
			if rerr != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_, _ = conn.Write([]byte("250-test\r\n250 SIZE 10240000\r\n")) // no STARTTLS advertised
			case strings.HasPrefix(line, "QUIT"):
				_, _ = conn.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 ok\r\n"))
			}
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := &config.Config{SMTPHost: host, SMTPPort: port, SMTPFrom: "mr@example.at", SMTPStartTLS: true}
	err = Send(cfg, "n@example.at", "s", "b", nil)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS-required rejection, got %v", err)
	}
}
