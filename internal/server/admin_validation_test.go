package server

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"treckrr/internal/config"
	"treckrr/internal/store"
)

var (
	registerOnce sync.Once
)

type testDriver struct{}

func (d *testDriver) Open(name string) (driver.Conn, error) {
	return &testConn{}, nil
}

type testConn struct{}

func (c *testConn) Prepare(query string) (driver.Stmt, error) {
	return &testStmt{query: query}, nil
}
func (c *testConn) Close() error { return nil }
func (c *testConn) Begin() (driver.Tx, error) { return &testTx{}, nil }

type testTx struct{}

func (t *testTx) Commit() error   { return nil }
func (t *testTx) Rollback() error { return nil }

type testStmt struct {
	query string
}

func (s *testStmt) Close() error { return nil }
func (s *testStmt) NumInput() int { return -1 }
func (s *testStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &testResult{}, nil
}
func (s *testStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &testRows{}, nil
}

type testResult struct{}

func (r *testResult) LastInsertId() (int64, error) { return 1, nil }
func (r *testResult) RowsAffected() (int64, error) { return 1, nil }

type testRows struct {
	hasRead bool
}

func (r *testRows) Columns() []string {
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}
func (r *testRows) Close() error { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	dest[0] = int64(123)
	dest[1] = "existing_user"
	dest[2] = "existing@example.com"
	dest[3] = "editor"
	dest[4] = false
	dest[5] = false
	dest[6] = false
	dest[7] = time.Now()
	return nil
}

func newTestServerWithMockStore() *Server {
	registerOnce.Do(func() {
		sql.Register("test_driver", &testDriver{})
	})
	db, _ := sql.Open("test_driver", "ignored")
	st := store.New(db, "test-secret-at-least-16")
	return &Server{
		cfg: &config.Config{
			SessionSecret: "test-secret-at-least-16",
		},
		store: st,
	}
}

func TestHandleUserCreate_UsernameLengthLimit(t *testing.T) {
	s := newTestServerWithMockStore()

	// 1. Test username with 101 characters (exceeds limit of 100)
	longUsername := strings.Repeat("a", 101)
	form := url.Values{}
	form.Set("username", longUsername)
	form.Set("password", "Pass1234")
	form.Set("role", "editor")

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleUserCreate(rr, req)

	resp := rr.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected redirect (303), got %d", resp.StatusCode)
	}

	foundCookie := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "treckrr_flash" {
			foundCookie = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "Benutzername darf höchstens 100 Zeichen lang sein.") {
				t.Errorf("expected flash message about length limit, got %q", decoded)
			}
		}
	}
	if !foundCookie {
		t.Error("expected treckrr_flash cookie to be set")
	}

	// 2. Test valid username (under 100 characters)
	validUsername := strings.Repeat("a", 99)
	formValid := url.Values{}
	formValid.Set("username", validUsername)
	formValid.Set("password", "Pass1234")
	formValid.Set("role", "editor")

	reqValid := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(formValid.Encode()))
	reqValid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rrValid := httptest.NewRecorder()

	s.handleUserCreate(rrValid, reqValid)

	respValid := rrValid.Result()
	for _, cookie := range respValid.Cookies() {
		if cookie.Name == "treckrr_flash" {
			decoded, _ := url.QueryUnescape(cookie.Value)
			if strings.Contains(decoded, "Benutzername darf höchstens 100 Zeichen lang sein.") {
				t.Errorf("did not expect error flash for valid username, got %q", decoded)
			}
		}
	}
}

func TestHandleUserUpdate_LengthLimits(t *testing.T) {
	s := newTestServerWithMockStore()

	// 1. Test username with 101 characters (exceeds limit of 100)
	longUsername := strings.Repeat("a", 101)
	form := url.Values{}
	form.Set("username", longUsername)
	form.Set("email", "test@example.com")

	req := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "123")
	rr := httptest.NewRecorder()

	s.handleUserUpdate(rr, req)

	resp := rr.Result()
	foundCookie := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "treckrr_flash" {
			foundCookie = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "Benutzername darf höchstens 100 Zeichen lang sein.") {
				t.Errorf("expected flash message about username length limit, got %q", decoded)
			}
		}
	}
	if !foundCookie {
		t.Error("expected treckrr_flash cookie to be set")
	}

	// 2. Test email with 255 characters (exceeds limit of 254)
	longEmail := strings.Repeat("a", 243) + "@example.com" // 243 + 12 = 255 chars
	formEmail := url.Values{}
	formEmail.Set("username", "valid_user")
	formEmail.Set("email", longEmail)

	reqEmail := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(formEmail.Encode()))
	reqEmail.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqEmail.SetPathValue("id", "123")
	rrEmail := httptest.NewRecorder()

	s.handleUserUpdate(rrEmail, reqEmail)

	respEmail := rrEmail.Result()
	foundEmailCookie := false
	for _, cookie := range respEmail.Cookies() {
		if cookie.Name == "treckrr_flash" {
			foundEmailCookie = true
			decoded, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(decoded, "E-Mail-Adresse darf höchstens 254 Zeichen lang sein.") {
				t.Errorf("expected flash message about email length limit, got %q", decoded)
			}
		}
	}
	if !foundEmailCookie {
		t.Error("expected treckrr_flash cookie to be set for long email")
	}

	// 3. Test valid username and email
	formValid := url.Values{}
	formValid.Set("username", "valid_user")
	formValid.Set("email", "valid@example.com")

	reqValid := httptest.NewRequest(http.MethodPost, "/admin/users/123/update", strings.NewReader(formValid.Encode()))
	reqValid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqValid.SetPathValue("id", "123")
	rrValid := httptest.NewRecorder()

	s.handleUserUpdate(rrValid, reqValid)

	respValid := rrValid.Result()
	for _, cookie := range respValid.Cookies() {
		if cookie.Name == "treckrr_flash" {
			decoded, _ := url.QueryUnescape(cookie.Value)
			if strings.Contains(decoded, "Zeichen lang sein.") {
				t.Errorf("did not expect error flash for valid input, got %q", decoded)
			}
		}
	}
}
