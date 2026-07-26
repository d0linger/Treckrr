package server

import (
	"context"
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

// Define a minimal custom sql driver.
type mockConn struct{}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	return &mockStmt{query: query}, nil
}

func (c *mockConn) Close() error { return nil }

func (c *mockConn) Begin() (driver.Tx, error) { return nil, nil }

type mockStmt struct {
	query string
}

func (s *mockStmt) Close() error { return nil }

func (s *mockStmt) NumInput() int { return -1 }

func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockResult{}, nil
}

func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "FROM users WHERE id=") {
		return &mockRows{
			cols: []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"},
			data: [][]driver.Value{
				{int64(123), "existing_user", "existing@example.com", models.RoleAdmin, true, false, false, time.Now()},
			},
		}, nil
	}
	return &mockRows{}, nil
}

type mockResult struct{}

func (r *mockResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockRows struct {
	cols  []string
	data  [][]driver.Value
	index int
}

func (r *mockRows) Columns() []string { return r.cols }

func (r *mockRows) Close() error { return nil }

func (r *mockRows) Next(dest []driver.Value) error {
	if r.index >= len(r.data) {
		return io.EOF
	}
	for i, v := range r.data[r.index] {
		dest[i] = v
	}
	r.index++
	return nil
}

type mockDriver struct{}

func (d *mockDriver) Open(name string) (driver.Conn, error) {
	return &mockConn{}, nil
}

func init() {
	sql.Register("mock_admin_driver", &mockDriver{})
}

func testAdminServer(db *sql.DB) *Server {
	cfg := &config.Config{
		SessionSecret: "test-secret-at-least-16",
	}
	st := store.New(db, "enc-key")
	s, _ := New(cfg, st)
	return s
}

func TestHandleUserCreate_UsernameLength(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	s := testAdminServer(db)

	tooLongUsername := strings.Repeat("a", 101)
	form := url.Values{}
	form.Set("username", tooLongUsername)
	form.Set("password", "Secure123")
	form.Set("role", models.RoleEditor)

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	s.handleUserCreate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected redirect status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	loc := rr.Header().Get("Location")
	if loc != "/admin/users" {
		t.Errorf("expected redirect location '/admin/users', got %q", loc)
	}

	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "Benutzername darf maximal 100 Zeichen lang sein") {
				t.Errorf("expected flash message about username limit, got %q", decoded)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set")
	}
}

func TestHandleUserUpdate_UsernameLength(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	s := testAdminServer(db)

	tooLongUsername := strings.Repeat("a", 101)
	form := url.Values{}
	form.Set("username", tooLongUsername)
	form.Set("email", "ok@example.com")

	req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "123")

	rr := httptest.NewRecorder()
	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected redirect status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "Benutzername darf maximal 100 Zeichen lang sein") {
				t.Errorf("expected flash message about username limit, got %q", decoded)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set")
	}
}

func TestHandleUserUpdate_EmailLength(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	s := testAdminServer(db)

	tooLongEmail := strings.Repeat("a", 243) + "@example.com" // 255 chars
	form := url.Values{}
	form.Set("username", "validname")
	form.Set("email", tooLongEmail)

	req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "123")

	rr := httptest.NewRecorder()
	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected redirect status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "E-Mail-Adresse darf maximal 254 Zeichen lang sein") {
				t.Errorf("expected flash message about email limit, got %q", decoded)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set")
	}
}

func TestHandleUserUpdate_ValidInputPasses(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	s := testAdminServer(db)

	form := url.Values{}
	form.Set("username", "newvalidname")
	form.Set("email", "new@example.com")

	req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "123")

	// Set context user to satisfy audit and render log (if any)
	ctx := context.WithValue(req.Context(), userCtxKey, &models.User{ID: 456, Username: "admin", Role: models.RoleAdmin})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected redirect status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "success|Zugangsdaten aktualisiert") {
				t.Errorf("expected success flash, got %q", decoded)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set")
	}
}
