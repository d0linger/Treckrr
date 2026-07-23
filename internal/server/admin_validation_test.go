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
	"treckrr/internal/store"
)

// Define a unique driver name so it doesn't conflict with any other mocks.
const mockDriverName = "mock_admin_driver"

type testMockDriver struct{}

func (d *testMockDriver) Open(name string) (driver.Conn, error) {
	return &testMockConn{}, nil
}

type testMockConn struct{}

func (c *testMockConn) Prepare(query string) (driver.Stmt, error) {
	return &testMockStmt{}, nil
}

func (c *testMockConn) Close() error {
	return nil
}

func (c *testMockConn) Begin() (driver.Tx, error) {
	return &testMockTx{}, nil
}

type testMockStmt struct{}

func (s *testMockStmt) Close() error {
	return nil
}

func (s *testMockStmt) NumInput() int {
	return -1
}

func (s *testMockStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &testMockResult{}, nil
}

func (s *testMockStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &testMockRows{}, nil
}

type testMockTx struct{}

func (t *testMockTx) Commit() error {
	return nil
}

func (t *testMockTx) Rollback() error {
	return nil
}

type testMockResult struct{}

func (r *testMockResult) LastInsertId() (int64, error) {
	return 1, nil
}

func (r *testMockResult) RowsAffected() (int64, error) {
	return 1, nil
}

type testMockRows struct {
	read bool
}

func (r *testMockRows) Columns() []string {
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}

func (r *testMockRows) Close() error {
	return nil
}

func (r *testMockRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(1)
	dest[1] = "existing_user"
	dest[2] = "user@example.com"
	dest[3] = "editor"
	dest[4] = false
	dest[5] = false
	dest[6] = false
	dest[7] = time.Now()
	return nil
}

func init() {
	sql.Register(mockDriverName, &testMockDriver{})
}

func TestHandleUserCreate_UsernameLengthValidation(t *testing.T) {
	dbConn, err := sql.Open(mockDriverName, "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer dbConn.Close()

	st := store.New(dbConn, "test-encryption-key-must-be-32-bytes-long!")
	s := &Server{
		cfg:   &config.Config{SessionSecret: "test-session-secret"},
		store: st,
	}

	// Too long username (101 characters)
	tooLongUsername := strings.Repeat("a", 101)

	form := url.Values{}
	form.Set("username", tooLongUsername)
	form.Set("password", "Pass1234")
	form.Set("role", "editor")

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleUserCreate(rr, req)

	// Check response. It should redirect back to /admin/users.
	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}
	loc := rr.Header().Get("Location")
	if loc != "/admin/users" {
		t.Errorf("expected redirect to /admin/users, got %q", loc)
	}

	// Check the flash cookie contains the expected error message.
	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			val, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(val, "Benutzername darf höchstens 100 Zeichen lang sein.") {
				t.Errorf("expected flash message about username length, got %q", val)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set")
	}
}

func TestHandleUserUpdate_LengthValidations(t *testing.T) {
	dbConn, err := sql.Open(mockDriverName, "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer dbConn.Close()

	st := store.New(dbConn, "test-encryption-key-must-be-32-bytes-long!")
	s := &Server{
		cfg:   &config.Config{SessionSecret: "test-session-secret"},
		store: st,
	}

	// 1. Test Username too long (101 characters)
	form := url.Values{}
	form.Set("username", strings.Repeat("a", 101))
	form.Set("email", "test@example.com")

	req := httptest.NewRequest(http.MethodPost, "/admin/users/1/update", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()

	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	c := rr.Result().Cookies()
	var flashFound bool
	for _, cookie := range c {
		if cookie.Name == flashCookie {
			flashFound = true
			val, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(val, "Benutzername darf höchstens 100 Zeichen lang sein.") {
				t.Errorf("expected flash message about username length, got %q", val)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set for username too long")
	}

	// 2. Test Email too long (255 characters)
	formEmail := url.Values{}
	formEmail.Set("username", "validname")
	// "a@b.com" with a prefix of 250 'a's -> total length 255
	formEmail.Set("email", strings.Repeat("a", 250)+"@b.com")

	reqEmail := httptest.NewRequest(http.MethodPost, "/admin/users/1/update", strings.NewReader(formEmail.Encode()))
	reqEmail.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqEmail.SetPathValue("id", "1")
	rrEmail := httptest.NewRecorder()

	s.handleUserUpdate(rrEmail, reqEmail)

	if rrEmail.Code != http.StatusSeeOther {
		t.Errorf("expected status %d, got %d", http.StatusSeeOther, rrEmail.Code)
	}

	cEmail := rrEmail.Result().Cookies()
	flashFound = false
	for _, cookie := range cEmail {
		if cookie.Name == flashCookie {
			flashFound = true
			val, _ := url.QueryUnescape(cookie.Value)
			if !strings.Contains(val, "E‑Mail‑Adresse darf höchstens 254 Zeichen lang sein.") {
				t.Errorf("expected flash message about email length, got %q", val)
			}
		}
	}
	if !flashFound {
		t.Error("expected flash cookie to be set for email too long")
	}
}
