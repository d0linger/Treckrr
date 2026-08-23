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

type mockIdemDriver struct{}

func (d *mockIdemDriver) Open(name string) (driver.Conn, error) {
	return &mockIdemConn{}, nil
}

type mockIdemConn struct{}

func (c *mockIdemConn) Prepare(query string) (driver.Stmt, error) {
	return &mockIdemStmt{query: query}, nil
}
func (c *mockIdemConn) Close() error              { return nil }
func (c *mockIdemConn) Begin() (driver.Tx, error) { return &mockIdemTx{}, nil }

type mockIdemTx struct{}

func (t *mockIdemTx) Commit() error   { return nil }
func (t *mockIdemTx) Rollback() error { return nil }

type mockIdemStmt struct {
	query string
}

func (s *mockIdemStmt) Close() error  { return nil }
func (s *mockIdemStmt) NumInput() int { return -1 }
func (s *mockIdemStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockIdemResult{}, nil
}
func (s *mockIdemStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockIdemRows{query: s.query}, nil
}

type mockIdemResult struct{}

func (r *mockIdemResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockIdemResult) RowsAffected() (int64, error) { return 1, nil }

type mockIdemRows struct {
	query   string
	hasRead bool
}

func (r *mockIdemRows) Columns() []string {
	if strings.Contains(r.query, "FROM billing_years") {
		return []string{"y_id", "y_year", "y_base_id", "y_label", "y_status", "y_created_at", "b_id", "b_year", "b_name", "b_locked", "b_created_at"}
	}
	if strings.Contains(r.query, "JOIN neighbors n") {
		return []string{"n_id", "n_name", "n_note", "n_address", "n_tax_id", "n_archived", "n_created_at"}
	}
	if strings.Contains(r.query, "FROM billing_year_neighbors") {
		return []string{"exists"}
	}
	if strings.Contains(r.query, "FROM invoices") {
		return []string{"id"}
	}
	return []string{"id"}
}

func (r *mockIdemRows) Close() error { return nil }

func (r *mockIdemRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "FROM billing_years") {
		dest[0] = int64(1)
		dest[1] = 2026
		dest[2] = int64(1)
		dest[3] = "2026"
		dest[4] = "in_progress"
		dest[5] = time.Now()
		dest[6] = int64(1)
		dest[7] = 2026
		dest[8] = "Basis 2026"
		dest[9] = false
		dest[10] = time.Now()
	} else if strings.Contains(r.query, "JOIN neighbors n") {
		dest[0] = int64(1)
		dest[1] = "Max Mustermann"
		dest[2] = ""
		dest[3] = ""
		dest[4] = ""
		dest[5] = false
		dest[6] = time.Now()
	} else if strings.Contains(r.query, "FROM billing_year_neighbors") {
		dest[0] = true // is member
	} else {
		return io.EOF
	}
	return nil
}

func init() {
	sql.Register("mock_idem", &mockIdemDriver{})
}

func testIdemServer(t *testing.T) *Server {
	db, err := sql.Open("mock_idem", "")
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

func TestOversizedIdempotencyKeyRejected(t *testing.T) {
	s := testIdemServer(t)
	oversized := strings.Repeat("k", maxNameLen+1)

	form := url.Values{}
	form.Set("neighbor_id", "1")
	form.Set("year_id", "1")
	form.Set("unit", "ha")
	form.Set("task_label", "Pflügen")
	form.Set("quantity", "10")
	form.Set("unit_price", "50")
	form.Set("idempotency_key", oversized)

	req := httptest.NewRequest(http.MethodPost, "/entries/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleEntryCreate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status SeeOther, got %d", rr.Code)
	}
	flashCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(flashCookie, url.QueryEscape("Idempotency-Key darf höchstens 100 Zeichen lang sein.")) {
		t.Errorf("expected oversized idempotency_key warning, got cookie: %q", flashCookie)
	}
}

func TestOversizedImportTokenRejected(t *testing.T) {
	s := testIdemServer(t)
	oversized := strings.Repeat("t", maxNameLen+1)

	form := url.Values{}
	form.Set("year_id", "1")
	form.Set("csv", "Nachbar;Datum;Tätigkeit\n")
	form.Set("import_token", oversized)

	req := httptest.NewRequest(http.MethodPost, "/entries/import/commit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleImportCommit(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status SeeOther, got %d", rr.Code)
	}
	flashCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(flashCookie, url.QueryEscape("Import-Token darf höchstens 100 Zeichen lang sein.")) {
		t.Errorf("expected oversized import_token warning, got cookie: %q", flashCookie)
	}
}
