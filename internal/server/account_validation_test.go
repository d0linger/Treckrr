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

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

type mockAccountDriver struct{}

func (d *mockAccountDriver) Open(name string) (driver.Conn, error) {
	return &mockAccountConn{}, nil
}

type mockAccountConn struct{}

func (c *mockAccountConn) Prepare(query string) (driver.Stmt, error) {
	return &mockAccountStmt{query: query}, nil
}
func (c *mockAccountConn) Close() error              { return nil }
func (c *mockAccountConn) Begin() (driver.Tx, error) { return &mockAccountTx{}, nil }

type mockAccountTx struct{}

func (t *mockAccountTx) Commit() error   { return nil }
func (t *mockAccountTx) Rollback() error { return nil }

type mockAccountStmt struct {
	query string
}

func (s *mockAccountStmt) Close() error  { return nil }
func (s *mockAccountStmt) NumInput() int { return -1 }
func (s *mockAccountStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockAccountResult{}, nil
}
func (s *mockAccountStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockAccountRows{query: s.query}, nil
}

type mockAccountResult struct{}

func (r *mockAccountResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockAccountResult) RowsAffected() (int64, error) { return 1, nil }

type mockAccountRows struct {
	query   string
	hasRead bool
}

var testAccountPasswordHash string

func (r *mockAccountRows) Columns() []string {
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at", "password_hash"}
}

func (r *mockAccountRows) Close() error { return nil }

func (r *mockAccountRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true

	dest[0] = int64(123)
	dest[1] = "testuser"
	dest[2] = "test@example.com"
	dest[3] = "editor"
	dest[4] = false
	dest[5] = false
	dest[6] = false
	dest[7] = time.Now()
	dest[8] = testAccountPasswordHash

	return nil
}

func init() {
	sql.Register("mock_account", &mockAccountDriver{})
	hash, _ := auth.HashPassword("SecurePassword123")
	testAccountPasswordHash = hash
}

func testAccountServer(t *testing.T) *Server {
	db, err := sql.Open("mock_account", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
	}
	return &Server{
		cfg:    cfg,
		store:  st,
		logins: newLoginLimiter(st),
	}
}

func TestHandleAccountPasswordSubmitValidation(t *testing.T) {
	s := testAccountServer(t)

	t.Run("same new password is rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("current_password", "SecurePassword123")
		form.Set("new_password", "SecurePassword123")
		form.Set("new_password_confirm", "SecurePassword123")

		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		// Inject user context directly
		ctx := context.WithValue(req.Context(), userCtxKey, &models.User{
			ID:       123,
			Username: "testuser",
			Role:     "editor",
		})
		req = req.WithContext(ctx)

		s.handleAccountPasswordSubmit(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Das neue Passwort darf nicht mit dem aktuellen Passwort übereinstimmen.")) {
			t.Errorf("expected identical password warning, got cookie: %q", flashCookie)
		}
	})

	t.Run("different new password is accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("current_password", "SecurePassword123")
		form.Set("new_password", "NewSecurePassword456")
		form.Set("new_password_confirm", "NewSecurePassword456")

		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		ctx := context.WithValue(req.Context(), userCtxKey, &models.User{
			ID:       123,
			Username: "testuser",
			Role:     "editor",
		})
		req = req.WithContext(ctx)

		s.handleAccountPasswordSubmit(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Passwort geändert. Andere Sitzungen wurden beendet.")) {
			t.Errorf("expected success password change, got cookie: %q", flashCookie)
		}
	})
}
