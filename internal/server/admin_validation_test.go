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

// Define a unique driver name so it doesn't conflict with "pgx" or other tests.
const mockDriverName = "mock_admin_test_driver"

func init() {
	sql.Register(mockDriverName, &mockDriver{})
}

type mockDriver struct{}

func (d *mockDriver) Open(name string) (driver.Conn, error) {
	return &mockConn{}, nil
}

type mockConn struct{}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	return &mockStmt{query: query}, nil
}

func (c *mockConn) Close() error { return nil }

func (c *mockConn) Begin() (driver.Tx, error) { return &mockTx{}, nil }

type mockStmt struct {
	query string
}

func (s *mockStmt) Close() error { return nil }

func (s *mockStmt) NumInput() int { return -1 }

func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockResult{}, nil
}

func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "users") {
		if strings.Contains(s.query, "INSERT") {
			return &mockIDRows{}, nil
		}
		// It's a SELECT userCols query for GetUser/ListUsers
		return &mockUserRows{}, nil
	}
	return &mockIDRows{}, nil
}

type mockTx struct{}

func (t *mockTx) Commit() error   { return nil }
func (t *mockTx) Rollback() error { return nil }

type mockResult struct{}

func (r *mockResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockIDRows struct {
	read bool
}

func (r *mockIDRows) Columns() []string {
	return []string{"id"}
}

func (r *mockIDRows) Close() error { return nil }

func (r *mockIDRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(456)
	return nil
}

type mockUserRows struct {
	read bool
}

func (r *mockUserRows) Columns() []string {
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}

func (r *mockUserRows) Close() error { return nil }

func (r *mockUserRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(123)
	dest[1] = "existing_user"
	dest[2] = "old@example.com"
	dest[3] = "editor"
	dest[4] = false
	dest[5] = false
	dest[6] = false
	dest[7] = time.Now()
	return nil
}

func getFlashFromRecorder(rr *httptest.ResponseRecorder) (msg, kind string) {
	resp := rr.Result()
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == flashCookie {
			parts := strings.SplitN(c.Value, "|", 2)
			if len(parts) != 2 {
				return "", ""
			}
			decoded, err := url.QueryUnescape(parts[1])
			if err != nil {
				return "", ""
			}
			return decoded, parts[0]
		}
	}
	return "", ""
}

func TestHandleUserCreate_Validation(t *testing.T) {
	db, err := sql.Open(mockDriverName, "unused_dsn")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	st := store.New(db, "session_secret_at_least_16_bytes")
	cfg := &config.Config{
		SessionSecret: "session_secret_at_least_16_bytes",
	}
	s, err := New(cfg, st)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	adminUser := &models.User{
		ID:       1,
		Username: "admin",
		Role:     models.RoleAdmin,
		IsAdmin:  true,
	}

	t.Run("Username too long", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("username", longUsername)
		form.Set("password", "ValidPass1")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %d", rr.Code)
		}

		// Verify the flash error was set and we redirected back
		loc := rr.Header().Get("Location")
		if loc != "/admin/users" {
			t.Errorf("expected redirect to /admin/users, got %q", loc)
		}

		// Decode the flash cookie to check the message
		flashMsg, flashKind := getFlashFromRecorder(rr)
		if flashKind != "error" {
			t.Errorf("expected flash kind 'error', got %q (msg: %q)", flashKind, flashMsg)
		}
		if !strings.Contains(flashMsg, "höchstens 100 Zeichen") {
			t.Errorf("expected too long error message, got %q", flashMsg)
		}
	})

	t.Run("Valid username", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "valid_user")
		form.Set("password", "ValidPass1")
		form.Set("role", "editor")

		req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		s.handleUserCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %d", rr.Code)
		}

		flashMsg, flashKind := getFlashFromRecorder(rr)
		if flashKind != "success" {
			t.Errorf("expected flash kind 'success', got %q (msg: %s)", flashKind, flashMsg)
		}
	})
}

func TestHandleUserUpdate_Validation(t *testing.T) {
	db, err := sql.Open(mockDriverName, "unused_dsn")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	st := store.New(db, "session_secret_at_least_16_bytes")
	cfg := &config.Config{
		SessionSecret: "session_secret_at_least_16_bytes",
	}
	s, err := New(cfg, st)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	adminUser := &models.User{
		ID:       1,
		Username: "admin",
		Role:     models.RoleAdmin,
		IsAdmin:  true,
	}

	t.Run("Username too long on update", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("username", longUsername)
		form.Set("email", "test@example.com")

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %d", rr.Code)
		}

		flashMsg, flashKind := getFlashFromRecorder(rr)
		if flashKind != "error" {
			t.Errorf("expected flash kind 'error', got %q", flashKind)
		}
		if !strings.Contains(flashMsg, "höchstens 100 Zeichen") {
			t.Errorf("expected too long error message, got %q", flashMsg)
		}
	})

	t.Run("Email too long on update", func(t *testing.T) {
		longEmail := strings.Repeat("a", 255) + "@example.com"
		form := url.Values{}
		form.Set("username", "someuser")
		form.Set("email", longEmail)

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %d", rr.Code)
		}

		flashMsg, flashKind := getFlashFromRecorder(rr)
		if flashKind != "error" {
			t.Errorf("expected flash kind 'error', got %q", flashKind)
		}
		if !strings.Contains(flashMsg, "höchstens 254 Zeichen") {
			t.Errorf("expected too long email error message, got %q", flashMsg)
		}
	})

	t.Run("Valid username and email on update", func(t *testing.T) {
		form := url.Values{}
		form.Set("username", "new_username")
		form.Set("email", "valid@example.com")

		req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "123")
		ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		s.handleUserUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %d", rr.Code)
		}

		flashMsg, flashKind := getFlashFromRecorder(rr)
		if flashKind != "success" {
			t.Errorf("expected flash kind 'success', got %q (msg: %s)", flashKind, flashMsg)
		}
	})
}
