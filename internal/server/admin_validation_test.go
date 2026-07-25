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

	"treckrr/internal/config"
	"treckrr/internal/models"
	"treckrr/internal/store"
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
func (c *mockConn) Begin() (driver.Tx, error) { return nil, nil }

type mockStmt struct {
	query string
}

func (s *mockStmt) Close() error                                    { return nil }
func (s *mockStmt) NumInput() int                                   { return -1 }
func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) { return &mockResult{}, nil }
func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockRows{query: s.query}, nil
}

type mockResult struct{}

func (r *mockResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockRows struct {
	query string
	step  int
}

func (r *mockRows) Columns() []string {
	if strings.Contains(r.query, "SELECT") {
		return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
	}
	return []string{"id"}
}

func (r *mockRows) Close() error { return nil }

func (r *mockRows) Next(dest []driver.Value) error {
	if r.step > 0 {
		return io.EOF
	}
	r.step++
	if strings.Contains(r.query, "SELECT") {
		// id, username, email, role, is_admin, must_change_password, totp_enabled, created_at
		dest[0] = int64(1)
		dest[1] = "existinguser"
		dest[2] = "user@example.com"
		dest[3] = "editor"
		dest[4] = false
		dest[5] = false
		dest[6] = false
		dest[7] = time.Now()
		return nil
	}
	dest[0] = int64(1)
	return nil
}

func init() {
	sql.Register("mockdriver", &mockDriver{})
}

func TestAdminValidation(t *testing.T) {
	db, err := sql.Open("mockdriver", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-must-be-at-least-32-chars-long")
	s, err := New(&config.Config{SessionSecret: "test-secret-at-least-16"}, st)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// 1. Test handleUserCreate with too long username
	t.Run("CreateUser_UsernameTooLong", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", strings.Repeat("a", 101))
		form.Set("password", "SecurePassword1")
		form.Set("role", models.RoleEditor)

		req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		s.handleUserCreate(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
		}

		if loc := rec.Header().Get("Location"); loc != "/admin/users" {
			t.Errorf("expected redirect to /admin/users, got %q", loc)
		}
	})

	// 2. Test handleUserUpdate with too long username
	t.Run("UpdateUser_UsernameTooLong", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", strings.Repeat("a", 101))
		form.Set("email", "test@example.com")

		req := httptest.NewRequest("POST", "/admin/users/1/update", strings.NewReader(form.Encode()))
		req.SetPathValue("id", "1")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		s.handleUserUpdate(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
		}

		if loc := rec.Header().Get("Location"); loc != "/admin/users" {
			t.Errorf("expected redirect to /admin/users, got %q", loc)
		}
	})

	// 3. Test handleUserUpdate with too long email
	t.Run("UpdateUser_EmailTooLong", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "validuser")
		form.Set("email", strings.Repeat("a", 243)+"@example.com") // Total length 255 (> 254)

		req := httptest.NewRequest("POST", "/admin/users/1/update", strings.NewReader(form.Encode()))
		req.SetPathValue("id", "1")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		s.handleUserUpdate(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
		}

		if loc := rec.Header().Get("Location"); loc != "/admin/users" {
			t.Errorf("expected redirect to /admin/users, got %q", loc)
		}
	})

	// 4. Test handleUserCreate with valid username
	t.Run("CreateUser_Valid", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "validuser")
		form.Set("password", "SecurePassword1")
		form.Set("role", models.RoleEditor)

		req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		s.handleUserCreate(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
		}
	})
}
