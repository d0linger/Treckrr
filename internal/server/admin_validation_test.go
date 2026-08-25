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

type mockDriver struct{}

func (d *mockDriver) Open(name string) (driver.Conn, error) {
	return &mockConn{}, nil
}

type mockConn struct{}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	return &mockStmt{query: query}, nil
}
func (c *mockConn) Close() error              { return nil }
func (c *mockConn) Begin() (driver.Tx, error) { return &mockTx{}, nil }

type mockTx struct{}

func (t *mockTx) Commit() error   { return nil }
func (t *mockTx) Rollback() error { return nil }

type mockStmt struct {
	query string
}

func (s *mockStmt) Close() error  { return nil }
func (s *mockStmt) NumInput() int { return -1 }
func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockResult{}, nil
}
func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockRows{query: s.query}, nil
}

type mockResult struct{}

func (r *mockResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockRows struct {
	query   string
	hasRead bool
}

func (r *mockRows) Columns() []string {
	if strings.Contains(r.query, "INSERT INTO users") {
		return []string{"id"}
	}
	// GetUser scan columns: id, username, email, role, is_admin, must_change_password, totp_enabled, created_at
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}

func (r *mockRows) Close() error { return nil }

func (r *mockRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "INSERT INTO users") {
		dest[0] = int64(42)
	} else {
		// GetUser expectations
		dest[0] = int64(123)
		dest[1] = "existinguser"
		dest[2] = "existing@example.com"
		dest[3] = "editor"
		dest[4] = false
		dest[5] = false
		dest[6] = false
		dest[7] = time.Now()
	}
	return nil
}

func init() {
	sql.Register("mock_admin", &mockDriver{})
}

func testAdminServer(t *testing.T) *Server {
	db, err := sql.Open("mock_admin", "")
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

func TestHandleUserCreateValidation(t *testing.T) {
	s := testAdminServer(t)

	t.Run("overly long username rejected", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("username", longUsername)
		form.Set("password", "SecurePassword123")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		if loc := rr.Header().Get("Location"); loc != "/admin/users" {
			t.Errorf("expected Location /admin/users, got %q", loc)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Benutzername darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected long username flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("empty username rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "")
		form.Set("password", "SecurePassword123")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Benutzername ist erforderlich.") {
			t.Errorf("expected empty username flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("exactly 100-char username accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", strings.Repeat("a", 100))
		form.Set("password", "SecurePassword123")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Benutzer angelegt.") {
			t.Errorf("expected success flash for 100-char username, got cookie: %q", flashCookie)
		}
	})

	t.Run("valid input accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "gooduser")
		form.Set("password", "SecurePassword123")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Benutzer angelegt.") {
			t.Errorf("expected success flash message, got cookie: %q", flashCookie)
		}
	})
}

func TestHandleUserUpdateValidation(t *testing.T) {
	s := testAdminServer(t)

	t.Run("overly long username rejected", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("username", longUsername)
		form.Set("email", "ok@example.com")

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		rr := httptest.NewRecorder()

		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Benutzername darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected long username flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long email rejected", func(t *testing.T) {
		longEmail := strings.Repeat("e", 255)
		form := url.Values{}
		form.Set("username", "okuser")
		form.Set("email", longEmail)

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		rr := httptest.NewRecorder()

		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "E‑Mail‑Adresse darf höchstens 254 Zeichen lang sein.") {
			t.Errorf("expected long email flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("exactly 100-char username and 254-char email accepted", func(t *testing.T) {
		maxEmail := strings.Repeat("a", 242) + "@example.com" // 254 chars, valid address
		form := url.Values{}
		form.Set("username", strings.Repeat("a", 100))
		form.Set("email", maxEmail)

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		rr := httptest.NewRecorder()

		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Zugangsdaten aktualisiert.") {
			t.Errorf("expected success flash for boundary input, got cookie: %q", flashCookie)
		}
	})

	t.Run("valid update accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "okuser")
		form.Set("email", "ok@example.com")

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		rr := httptest.NewRecorder()

		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Zugangsdaten aktualisiert.") {
			t.Errorf("expected success flash message, got cookie: %q", flashCookie)
		}
	})
}
