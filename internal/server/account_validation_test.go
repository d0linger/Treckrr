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
	"sync/atomic"
	"testing"

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
	if strings.Contains(s.query, "INSERT INTO login_attempts") {
		testAccountAdmissionQueries.Add(1)
	}
	return &mockAccountRows{query: s.query}, nil
}

type mockAccountResult struct{}

func (r *mockAccountResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockAccountResult) RowsAffected() (int64, error) { return 1, nil }

type mockAccountRows struct {
	query   string
	hasRead bool
}

var (
	testAccountPasswordHash     string
	testAccountAdmissionQueries atomic.Int64
)

func (r *mockAccountRows) Columns() []string {
	return []string{"value"}
}

func (r *mockAccountRows) Close() error { return nil }

func (r *mockAccountRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true

	switch {
	case strings.Contains(r.query, "INSERT INTO login_attempts"):
		dest[0] = int64(1)
	case strings.Contains(r.query, "SELECT password_hash"):
		dest[0] = testAccountPasswordHash
	case strings.Contains(r.query, "SELECT EXISTS"):
		dest[0] = true
	default:
		return io.EOF
	}

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
		testAccountAdmissionQueries.Store(0)
		form := url.Values{}
		form.Set("current_password", "SecurePassword123")
		form.Set("new_password", "SecurePassword123")
		form.Set("new_password_confirm", "SecurePassword123")

		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "original-session"})
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
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Das neue Passwort darf nicht mit dem aktuellen Passwort übereinstimmen.") {
			t.Errorf("expected identical password warning, got cookie: %q", flashCookie)
		}
		if got := testAccountAdmissionQueries.Load(); got != 0 {
			t.Errorf("local validation consumed %d admission attempts", got)
		}
	})

	t.Run("mismatched confirmation is rejected without admission", func(t *testing.T) {
		testAccountAdmissionQueries.Store(0)
		next := "NewSecurePassword456"
		confirmation := "DifferentPassword789"
		form := url.Values{
			"current_password":     {"SecurePassword123"},
			"new_password":         {next},
			"new_password_confirm": {confirmation},
		}
		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor"}))
		rr := httptest.NewRecorder()

		s.handleAccountPasswordSubmit(rr, req)

		if got := testAccountAdmissionQueries.Load(); got != 0 {
			t.Errorf("local validation consumed %d admission attempts", got)
		}
	})

	t.Run("password policy rejection does not consume admission", func(t *testing.T) {
		testAccountAdmissionQueries.Store(0)
		form := url.Values{
			"current_password":     {"SecurePassword123"},
			"new_password":         {"short"},
			"new_password_confirm": {"short"},
		}
		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor"}))
		rr := httptest.NewRecorder()

		s.handleAccountPasswordSubmit(rr, req)

		if got := testAccountAdmissionQueries.Load(); got != 0 {
			t.Errorf("local validation consumed %d admission attempts", got)
		}
	})

	t.Run("different new password is accepted", func(t *testing.T) {
		testAccountAdmissionQueries.Store(0)
		form := url.Values{}
		form.Set("current_password", "SecurePassword123")
		form.Set("new_password", "NewSecurePassword456")
		form.Set("new_password_confirm", "NewSecurePassword456")

		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "original-session"})
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
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Passwort geändert. Andere Sitzungen wurden beendet.") {
			t.Errorf("expected success password change, got cookie: %q", flashCookie)
		}
		found := false
		for _, cookie := range rr.Result().Cookies() {
			if cookie.Name == sessionCookie && cookie.Value != "" && cookie.Value != "original-session" {
				found = true
			}
		}
		if !found {
			t.Error("fresh session cookie missing after password change")
		}
		if got := testAccountAdmissionQueries.Load(); got != 1 {
			t.Errorf("password verification consumed %d admission attempts, want 1", got)
		}
	})
}

func TestHandleTwoFactorConfirmValidation(t *testing.T) {
	s := testAccountServer(t)

	t.Run("oversized password in confirm is rejected", func(t *testing.T) {
		form := url.Values{"password": {strings.Repeat("a", 73)}, "code": {"123456"}}
		req := httptest.NewRequest(http.MethodPost, "/account/2fa", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor"}))
		rr := httptest.NewRecorder()

		s.handleTwoFactorConfirm(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Passwort darf höchstens 72 Zeichen lang sein.") {
			t.Errorf("expected over-limit password warning, got: %q", flashCookie)
		}
	})

	t.Run("oversized code in confirm is rejected", func(t *testing.T) {
		form := url.Values{"password": {"SecurePassword123"}, "code": {strings.Repeat("1", maxNameLen+1)}}
		req := httptest.NewRequest(http.MethodPost, "/account/2fa", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor"}))
		rr := httptest.NewRecorder()

		s.handleTwoFactorConfirm(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Code darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected over-limit code warning, got: %q", flashCookie)
		}
	})

	t.Run("missing pending secret is handled", func(t *testing.T) {
		testAccountAdmissionQueries.Store(0)
		password := "SecurePassword123"
		form := url.Values{"password": {password}, "code": {"123456"}}
		req := httptest.NewRequest(http.MethodPost, "/account/2fa", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor"}))
		rr := httptest.NewRecorder()

		s.handleTwoFactorConfirm(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected status SeeOther, got %v", rr.Code)
		}
		if got := testAccountAdmissionQueries.Load(); got != 0 {
			t.Errorf("missing pending secret consumed %d admission attempts", got)
		}
	})
}

func TestHandleRecoveryRegenerateValidation(t *testing.T) {
	s := testAccountServer(t)

	t.Run("oversized password in regenerate is rejected", func(t *testing.T) {
		form := url.Values{"password": {strings.Repeat("a", 73)}}
		req := httptest.NewRequest(http.MethodPost, "/account/2fa/recovery", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor", TotpEnabled: true}))
		rr := httptest.NewRecorder()

		s.handleRecoveryRegenerate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Passwort darf höchstens 72 Zeichen lang sein.") {
			t.Errorf("expected over-limit password warning, got: %q", flashCookie)
		}
	})
}

func TestHandleTwoFactorDisableValidation(t *testing.T) {
	s := testAccountServer(t)

	t.Run("oversized password in disable is rejected", func(t *testing.T) {
		form := url.Values{"password": {strings.Repeat("a", 73)}}
		req := httptest.NewRequest(http.MethodPost, "/account/2fa/disable", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 123, Username: "testuser", Role: "editor", TotpEnabled: true}))
		rr := httptest.NewRecorder()

		s.handleTwoFactorDisable(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Passwort darf höchstens 72 Zeichen lang sein.") {
			t.Errorf("expected over-limit password warning, got: %q", flashCookie)
		}
	})
}

func TestHandleSessionRevokeValidation(t *testing.T) {
	s := testAccountServer(t)

	t.Run("oversized session token is rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("token", strings.Repeat("a", maxNameLen+1))

		req := httptest.NewRequest(http.MethodPost, "/account/sessions/revoke", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		ctx := context.WithValue(req.Context(), userCtxKey, &models.User{
			ID:       123,
			Username: "testuser",
			Role:     "editor",
		})
		req = req.WithContext(ctx)

		s.handleSessionRevoke(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Token darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected over-limit token warning, got cookie: %q", flashCookie)
		}
	})
}
