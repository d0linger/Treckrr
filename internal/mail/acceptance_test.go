package mail

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
)

func TestSendDataAcknowledgementIsDeliveryBoundary(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted then disconnected"}[accepted], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
				reader := bufio.NewReader(conn)
				inData := false
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if inData {
						if line != ".\r\n" {
							continue
						}
						response := "550 rejected\r\n"
						if accepted {
							response = "250 accepted\r\n"
						}
						_, _ = conn.Write([]byte(response))
						return // deliberately no QUIT reply
					}
					if strings.HasPrefix(line, "DATA") {
						inData = true
						_, _ = conn.Write([]byte("354 send data\r\n"))
					} else {
						_, _ = conn.Write([]byte("250 ok\r\n"))
					}
				}
			}()
			host, port, _ := net.SplitHostPort(ln.Addr().String())
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = Send(ctx, &config.Config{SMTPHost: host, SMTPPort: port, SMTPFrom: "test@example.invalid"}, "to@example.invalid", "subject", "body", nil)
			if (err == nil) != accepted {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("SMTP server did not exit")
			}
		})
	}
}
