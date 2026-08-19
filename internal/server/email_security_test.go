package server

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

type mockEmailDriver struct{}

func (d *mockEmailDriver) Open(name string) (driver.Conn, error) {
	return &mockEmailConn{}, nil
}

type mockEmailConn struct{}

func (c *mockEmailConn) Prepare(query string) (driver.Stmt, error) {
	return &mockEmailStmt{query: query}, nil
}
func (c *mockEmailConn) Close() error              { return nil }
func (c *mockEmailConn) Begin() (driver.Tx, error) { return &mockEmailTx{}, nil }

type mockEmailTx struct{}

func (t *mockEmailTx) Commit() error   { return nil }
func (t *mockEmailTx) Rollback() error { return nil }

type mockEmailStmt struct {
	query string
}

func (s *mockEmailStmt) Close() error  { return nil }
func (s *mockEmailStmt) NumInput() int { return -1 }
func (s *mockEmailStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockEmailResult{}, nil
}
func (s *mockEmailStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockEmailRows{query: s.query}, nil
}

type mockEmailResult struct{}

func (r *mockEmailResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockEmailResult) RowsAffected() (int64, error) { return 1, nil }

type mockEmailRows struct {
	query   string
	hasRead bool
}

func (r *mockEmailRows) Columns() []string {
	q := strings.ToLower(r.query)
	switch {
	case strings.Contains(q, "content_hash"):
		return []string{
			"id", "billing_year_id", "neighbor_id", "number", "issued_on", "created_at",
			"kind", "status", "references_invoice_id", "payment_reference",
			"net", "vat_rate", "vat_amount", "gross", "show_vat", "tax_mode", "tax_note",
			"service_period_from", "service_period_to", "issuer_json", "recipient_json", "lines_json", "content_hash",
		}
	case strings.Contains(q, "payments p"):
		return []string{"net", "paid"}
	case strings.Contains(q, "company"):
		return []string{"name", "address", "tax_id", "tax_note", "tax_mode", "vat_rate", "iban", "payment_term_days"}
	case strings.Contains(q, "from billing_years"):
		return []string{"y_id", "y_year", "y_base_id", "y_label", "y_status", "y_created", "b_id", "b_year", "b_name", "b_locked", "b_created"}
	case strings.Contains(q, "from neighbors"):
		return []string{"id", "name", "note", "address", "tax_id", "email", "archived", "anonymized", "created_at"}
	default:
		return []string{"val"}
	}
}

func (r *mockEmailRows) Close() error { return nil }

func (r *mockEmailRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	q := strings.ToLower(r.query)

	switch {
	case strings.Contains(q, "content_hash"):
		dest[0] = int64(1)
		dest[1] = int64(1)
		dest[2] = int64(1)
		dest[3] = "RE-2026-001"
		dest[4] = time.Now()
		dest[5] = time.Now()
		dest[6] = "invoice"
		dest[7] = "issued"
		dest[8] = nil
		dest[9] = ""
		dest[10] = "100.00"
		dest[11] = "20"
		dest[12] = "20.00"
		dest[13] = "120.00"
		dest[14] = true
		dest[15] = "regel"
		dest[16] = ""
		dest[17] = time.Now()
		dest[18] = time.Now()
		dest[19] = []byte(`{}`)
		dest[20] = []byte(`{}`)
		dest[21] = []byte(`[]`)
		dest[22] = "hash"
	case strings.Contains(q, "payments p"):
		dest[0] = "100.00"
		dest[1] = "0.00"
	case strings.Contains(q, "company"):
		dest[0] = "Test Company"
		dest[1] = "Company Addr"
		dest[2] = "ATU12345678"
		dest[3] = ""
		dest[4] = "regel"
		dest[5] = "20"
		dest[6] = "AT123456789012345678"
		dest[7] = int64(14)
	case strings.Contains(q, "from billing_years"):
		dest[0] = int64(1)
		dest[1] = 2026
		dest[2] = int64(1)
		dest[3] = "2026"
		dest[4] = "active"
		dest[5] = time.Now()
		dest[6] = int64(1)
		dest[7] = 2026
		dest[8] = "Basis 2026"
		dest[9] = false
		dest[10] = time.Now()
	case strings.Contains(q, "from neighbors"):
		dest[0] = int64(1)
		dest[1] = "Test Neighbor"
		dest[2] = ""
		dest[3] = "Test Address"
		dest[4] = ""
		dest[5] = "neighbor@example.com"
		dest[6] = false
		dest[7] = false
		dest[8] = time.Now()
	default:
		dest[0] = "100.00"
	}
	return nil
}

func init() {
	sql.Register("mock_email_security", &mockEmailDriver{})
}

func testEmailServer(t *testing.T) *Server {
	db, err := sql.Open("mock_email_security", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
		SMTPHost:      "127.0.0.1",
		SMTPPort:      "1", // Unreachable port triggers connection error in mail.Send
		SMTPFrom:      "sender@example.com",
	}
	return &Server{
		cfg:   cfg,
		store: st,
	}
}

func TestEmailSendFailureDoesNotLeakInternalErrors(t *testing.T) {
	s := testEmailServer(t)

	t.Run("beleg email failure sanitizes error message", func(t *testing.T) {
		form := url.Values{}
		form.Set("year", "1")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/beleg/email", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleBelegEmail(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Versand fehlgeschlagen.")) {
			t.Errorf("expected generic error flash, got cookie: %q", flashCookie)
		}
		if strings.Contains(flashCookie, "connection") || strings.Contains(flashCookie, "refused") || strings.Contains(flashCookie, "dial") {
			t.Errorf("flash cookie leaks internal error details: %q", flashCookie)
		}
	})

	t.Run("mahnung email failure sanitizes error message", func(t *testing.T) {
		form := url.Values{}
		form.Set("year", "1")
		form.Set("stufe", "0")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/mahnung/email", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleMahnungEmail(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Versand fehlgeschlagen.")) {
			t.Errorf("expected generic error flash, got cookie: %q", flashCookie)
		}
		if strings.Contains(flashCookie, "connection") || strings.Contains(flashCookie, "refused") || strings.Contains(flashCookie, "dial") {
			t.Errorf("flash cookie leaks internal error details: %q", flashCookie)
		}
	})
}
