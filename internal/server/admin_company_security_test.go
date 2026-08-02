package server

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"treckrr/internal/config"
	"treckrr/internal/store"
)

var currentTestRole = "viewer"

type mockCompanySecDriver struct{}

func (d *mockCompanySecDriver) Open(name string) (driver.Conn, error) {
	return &mockCompanySecConn{}, nil
}

type mockCompanySecConn struct{}

func (c *mockCompanySecConn) Prepare(query string) (driver.Stmt, error) {
	return &mockCompanySecStmt{query: query}, nil
}
func (c *mockCompanySecConn) Close() error              { return nil }
func (c *mockCompanySecConn) Begin() (driver.Tx, error) { return &mockCompanySecTx{}, nil }

type mockCompanySecTx struct{}

func (t *mockCompanySecTx) Commit() error   { return nil }
func (t *mockCompanySecTx) Rollback() error { return nil }

type mockCompanySecStmt struct {
	query string
}

func (s *mockCompanySecStmt) Close() error  { return nil }
func (s *mockCompanySecStmt) NumInput() int { return -1 }
func (s *mockCompanySecStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockCompanySecResult{}, nil
}
func (s *mockCompanySecStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockCompanySecRows{query: s.query}, nil
}

type mockCompanySecResult struct{}

func (r *mockCompanySecResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockCompanySecResult) RowsAffected() (int64, error) { return 1, nil }

type mockCompanySecRows struct {
	query   string
	hasRead bool
}

func (r *mockCompanySecRows) Columns() []string {
	if strings.Contains(r.query, "company") {
		return []string{"name", "address", "tax_id", "tax_note", "tax_mode", "vat_rate"}
	}
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}

func (r *mockCompanySecRows) Close() error { return nil }

func (r *mockCompanySecRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "company") {
		dest[0] = "Test Company"
		dest[1] = "Test Address"
		dest[2] = "Tax123"
		dest[3] = "Note"
		dest[4] = "pauschal"
		dest[5] = float64(0.0)
	} else {
		dest[0] = int64(123)
		dest[1] = "testuser"
		dest[2] = "test@example.com"
		dest[3] = currentTestRole
		dest[4] = currentTestRole == "admin"
		dest[5] = false
		dest[6] = false
		dest[7] = time.Now()
	}
	return nil
}

func init() {
	sql.Register("mock_company_sec", &mockCompanySecDriver{})
}

func TestCompanySecurity(t *testing.T) {
	db, err := sql.Open("mock_company_sec", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
	}
	s, err := New(cfg, st, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	h := s.Handler()

	t.Run("unauthenticated GET /admin/company redirects to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/company", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected StatusSeeOther (303), got %v", rr.Code)
		}
		if loc := rr.Header().Get("Location"); loc != "/login" {
			t.Errorf("expected redirect to /login, got %q", loc)
		}
	})

	t.Run("admin GET /admin/company is allowed", func(t *testing.T) {
		currentTestRole = "admin"
		req := httptest.NewRequest(http.MethodGet, "/admin/company", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "some-session-token"})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected StatusOK (200), got %v", rr.Code)
		}
	})

	t.Run("editor GET /admin/company is forbidden", func(t *testing.T) {
		currentTestRole = "editor"
		req := httptest.NewRequest(http.MethodGet, "/admin/company", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "some-session-token"})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("expected StatusForbidden (403), got %v", rr.Code)
		}
	})

	t.Run("viewer GET /admin/company is forbidden", func(t *testing.T) {
		currentTestRole = "viewer"
		req := httptest.NewRequest(http.MethodGet, "/admin/company", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "some-session-token"})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("expected StatusForbidden (403), got %v", rr.Code)
		}
	})
}
