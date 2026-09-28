package mail

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
)

// fakeDataServer accepts one SMTP session and reports whether DATA was issued.
func fakeDataServer(t *testing.T) (*config.Config, *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var sawData atomic.Bool
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
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
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_, _ = conn.Write([]byte("250-test\r\n250 SIZE 10240000\r\n"))
			case strings.HasPrefix(line, "DATA"):
				sawData.Store(true)
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
	return &config.Config{SMTPHost: host, SMTPPort: port, SMTPFrom: "mr@example.at"}, &sawData
}

// TestDataStartHookRunsBeforeData verifies the hook fires exactly once and
// before the server sees DATA.
func TestDataStartHookRunsBeforeData(t *testing.T) {
	cfg, sawData := fakeDataServer(t)
	var calls atomic.Int32
	ctx := WithDataStartHook(context.Background(), func(context.Context) error {
		if sawData.Load() {
			t.Error("hook ran after DATA")
		}
		calls.Add(1)
		return nil
	})
	if err := Send(ctx, cfg, "n@example.at", "s", "b", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if calls.Load() != 1 || !sawData.Load() {
		t.Fatalf("hook calls=%d, data seen=%v", calls.Load(), sawData.Load())
	}
}

// TestFailedDataStartHookAbortsBeforeData verifies a failed marker write stops
// the send before DATA and is reported as a definite, retryable failure.
func TestFailedDataStartHookAbortsBeforeData(t *testing.T) {
	cfg, sawData := fakeDataServer(t)
	marker := errors.New("marker write failed")
	ctx := WithDataStartHook(context.Background(), func(context.Context) error { return marker })
	err := Send(ctx, cfg, "n@example.at", "s", "b", nil)
	if !errors.Is(err, marker) || IsAmbiguous(err) {
		t.Fatalf("err=%v ambiguous=%v", err, IsAmbiguous(err))
	}
	if sawData.Load() {
		t.Fatal("DATA was issued although the marker failed")
	}
}
