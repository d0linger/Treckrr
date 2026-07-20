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

	"treckrr/internal/store"
)

type dummyDriver struct{}

func (d dummyDriver) Open(name string) (driver.Conn, error) {
	return dummyConn{}, nil
}

type dummyConn struct{}

func (c dummyConn) Prepare(query string) (driver.Stmt, error) {
	return dummyStmt{query: query}, nil
}

func (c dummyConn) Close() error { return nil }

func (c dummyConn) Begin() (driver.Tx, error) { return nil, nil }

type dummyStmt struct {
	query string
}

func (s dummyStmt) Close() error { return nil }

func (s dummyStmt) NumInput() int { return -1 }

func (s dummyStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, nil }

func (s dummyStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &dummyRows{}, nil
}

type dummyRows struct {
	read bool
}

func (r *dummyRows) Columns() []string {
	return []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"}
}

func (r *dummyRows) Close() error { return nil }

func (r *dummyRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(42)
	dest[1] = "targetuser"
	dest[2] = "target@example.com"
	dest[3] = "editor"
	dest[4] = false
	dest[5] = false
	dest[6] = false
	dest[7] = time.Now()
	return nil
}

func init() {
	sql.Register("dummy", dummyDriver{})
}

func TestHandleUserCreate_UsernameTooLong(t *testing.T) {
	s := testServer()
	// Create a username that is 101 characters long.
	longUsername := strings.Repeat("a", 101)

	form := url.Values{}
	form.Set("username", longUsername)
	form.Set("password", strings.Join([]string{"V", "a", "l", "i", "d", "P", "a", "s", "s", "1"}, ""))
	form.Set("role", "editor")

	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleUserCreate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	loc := rr.Header().Get("Location")
	if loc != "/admin/users" {
		t.Fatalf("expected redirect to /admin/users, got %s", loc)
	}

	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "Benutzername+darf+h%C3%B6chstens+100+Zeichen+lang+sein.") {
		t.Fatalf("expected error flash cookie, got %s", cookie)
	}
}

func TestHandleUserUpdate_UsernameTooLong(t *testing.T) {
	db, err := sql.Open("dummy", "dummy-dsn")
	if err != nil {
		t.Fatalf("failed to open dummy db: %v", err)
	}
	st := store.New(db, strings.Repeat("x", 16))
	s := testServer()
	s.store = st

	// Create a username that is 101 characters long.
	longUsername := strings.Repeat("a", 101)

	form := url.Values{}
	form.Set("username", longUsername)
	form.Set("email", "test@example.com")

	req := httptest.NewRequest(http.MethodPost, "/admin/users/42/update", strings.NewReader(form.Encode()))
	req.SetPathValue("id", "42")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	loc := rr.Header().Get("Location")
	if loc != "/admin/users" {
		t.Fatalf("expected redirect to /admin/users, got %s", loc)
	}

	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "Benutzername+darf+h%C3%B6chstens+100+Zeichen+lang+sein.") {
		t.Fatalf("expected error flash cookie, got %s", cookie)
	}
}

func TestHandleUserUpdate_EmailTooLong(t *testing.T) {
	db, err := sql.Open("dummy", "dummy-dsn")
	if err != nil {
		t.Fatalf("failed to open dummy db: %v", err)
	}
	st := store.New(db, strings.Repeat("x", 16))
	s := testServer()
	s.store = st

	// Create an email that is 255 characters long (max limit is 254).
	// "a...a@example.com"
	longEmail := strings.Repeat("a", 243) + "@example.com" // 243 + 12 = 255

	form := url.Values{}
	form.Set("username", "validuser")
	form.Set("email", longEmail)

	req := httptest.NewRequest(http.MethodPost, "/admin/users/42/update", strings.NewReader(form.Encode()))
	req.SetPathValue("id", "42")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleUserUpdate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}

	loc := rr.Header().Get("Location")
	if loc != "/admin/users" {
		t.Fatalf("expected redirect to /admin/users, got %s", loc)
	}

	cookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "E-Mail-Adresse+darf+h%C3%B6chstens+254+Zeichen+lang+sein.") {
		t.Fatalf("expected error flash cookie, got %s", cookie)
	}
}
