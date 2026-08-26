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

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

type mockPaymentDriver struct{}

func (d *mockPaymentDriver) Open(name string) (driver.Conn, error) {
	return &mockPaymentConn{}, nil
}

type mockPaymentConn struct{}

func (c *mockPaymentConn) Prepare(query string) (driver.Stmt, error) {
	return &mockPaymentStmt{query: query}, nil
}
func (c *mockPaymentConn) Close() error              { return nil }
func (c *mockPaymentConn) Begin() (driver.Tx, error) { return &mockPaymentTx{}, nil }

type mockPaymentTx struct{}

func (t *mockPaymentTx) Commit() error   { return nil }
func (t *mockPaymentTx) Rollback() error { return nil }

type mockPaymentStmt struct {
	query string
}

func (s *mockPaymentStmt) Close() error  { return nil }
func (s *mockPaymentStmt) NumInput() int { return -1 }
func (s *mockPaymentStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockPaymentResult{}, nil
}

func (s *mockPaymentStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockPaymentRows{query: s.query}, nil
}

type mockPaymentResult struct{}

func (r *mockPaymentResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockPaymentResult) RowsAffected() (int64, error) { return 1, nil }

type mockPaymentRows struct {
	query   string
	hasRead bool
}

func (r *mockPaymentRows) Columns() []string {
	if strings.Contains(r.query, "SELECT EXISTS") {
		return []string{"exists"}
	}
	return []string{"id"}
}

func (r *mockPaymentRows) Close() error { return nil }

func (r *mockPaymentRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "SELECT EXISTS") {
		dest[0] = true // NeighborInYear membership exists
	} else {
		dest[0] = int64(1)
	}
	return nil
}

func init() {
	sql.Register("mock_payment", &mockPaymentDriver{})
}

func testPaymentServer(t *testing.T) *Server {
	db, err := sql.Open("mock_payment", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
	}
	return &Server{
		cfg:   cfg,
		store: st,
	}
}

func TestHandlePaymentAddValidation(t *testing.T) {
	s := testPaymentServer(t)

	t.Run("overly long paid_on rejected", func(t *testing.T) {
		// Exactly ONE char over the 50-char cap, so the boundary itself is
		// pinned — a cap loosened to 51+ fails this test.
		longDate := strings.Repeat("2026-01-01", 5) + "X" // 51 chars
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("amount", "100.00")
		form.Set("paid_on", longDate)
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/payments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handlePaymentAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Datum darf höchstens 50 Zeichen lang sein.") {
			t.Errorf("expected long date flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long skonto rejected", func(t *testing.T) {
		longSkonto := strings.Repeat("2", maxDecimalLen+1)
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("amount", "100.00")
		form.Set("paid_on", "2026-03-30")
		form.Set("note", "Valid Note")
		form.Set("skonto", longSkonto)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/payments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handlePaymentAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Skonto darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long skonto flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("valid paid_on accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("amount", "100.00")
		form.Set("paid_on", "2026-03-30")
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/payments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handlePaymentAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Zahlung erfasst.") {
			t.Errorf("expected success flash message, got cookie: %q", flashCookie)
		}
	})
}
