package mail

import (
	"bufio"
	"net"
	netmail "net/mail"
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

// TestBuildMessageNormalizesCommentCRLF: a comment-form value like
// "x@y (junk\r\nBcc: …)" is ACCEPTED by net/mail.ParseAddress but must not carry
// its CRLF into the To header. buildMessage is fed the parsed .Address, so the
// rendered message contains only the bare address and no injected header.
func TestBuildMessageNormalizesCommentCRLF(t *testing.T) {
	raw := "kunde@example.at (x\r\nBcc: leak@evil.com)"
	parsed, err := netmail.ParseAddress(raw)
	if err != nil {
		t.Fatalf("ParseAddress unexpectedly rejected %q: %v", raw, err)
	}
	// Send feeds the parsed .Address (not the raw input) to buildMessage.
	msg := string(buildMessage("mr@example.at", parsed.Address, "s", "b", nil))
	if strings.Contains(msg, "Bcc:") || strings.Contains(msg, "leak@evil.com") {
		t.Fatalf("injected header leaked into message:\n%s", msg)
	}
	if !strings.Contains(msg, "To: kunde@example.at") {
		t.Fatalf("expected clean To header, got:\n%s", msg)
	}
}

// TestSendEnvelopeFromIsBareAddress: a display-name SMTP_FROM ("MR <mr@x.at>") is
// fine for the From: header but the SMTP envelope (MAIL FROM) must carry only the
// bare address, or the server rejects it. Drives a fake SMTP server that records
// the MAIL FROM line and the DATA body.
func TestSendEnvelopeFromIsBareAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan struct{ mailFrom, body string }, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
		var mailFrom string
		var body strings.Builder
		inData := false
		for {
			line, rerr := br.ReadString('\n')
			if rerr != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					_, _ = conn.Write([]byte("250 ok\r\n"))
					got <- struct{ mailFrom, body string }{mailFrom, body.String()}
					continue
				}
				body.WriteString(line)
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_, _ = conn.Write([]byte("250-test\r\n250 SIZE 10240000\r\n"))
			case strings.HasPrefix(line, "MAIL FROM:"):
				mailFrom = strings.TrimSpace(line[len("MAIL FROM:"):])
				_, _ = conn.Write([]byte("250 ok\r\n"))
			case strings.HasPrefix(line, "DATA"):
				_, _ = conn.Write([]byte("354 go\r\n"))
				inData = true
			case strings.HasPrefix(line, "QUIT"):
				_, _ = conn.Write([]byte("221 bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 ok\r\n"))
			}
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := &config.Config{SMTPHost: host, SMTPPort: port, SMTPFrom: "Maschinenring <mr@example.at>", SMTPStartTLS: false}
	if err := Send(cfg, "n@example.at", "s", "b", nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	res := <-got
	if res.mailFrom != "<mr@example.at>" {
		t.Errorf("envelope MAIL FROM = %q, want %q (bare address, no display name)", res.mailFrom, "<mr@example.at>")
	}
	if !strings.Contains(res.body, "From: Maschinenring <mr@example.at>") {
		t.Errorf("From: header should keep the display name, got body:\n%s", res.body)
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
