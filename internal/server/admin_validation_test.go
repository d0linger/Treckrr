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

type mockDriverAdmin struct{}

func (d *mockDriverAdmin) Open(name string) (driver.Conn, error) {
	return &mockConnAdmin{}, nil
}

type mockConnAdmin struct{}

func (c *mockConnAdmin) Prepare(query string) (driver.Stmt, error) {
	return &mockStmtAdmin{query: query}, nil
}

func (c *mockConnAdmin) Close() error              { return nil }
func (c *mockConnAdmin) Begin() (driver.Tx, error) { return &mockTxAdmin{}, nil }

type mockTxAdmin struct{}

func (t *mockTxAdmin) Commit() error   { return nil }
func (t *mockTxAdmin) Rollback() error { return nil }

type mockStmtAdmin struct {
	query string
}

func (s *mockStmtAdmin) Close() error  { return nil }
func (s *mockStmtAdmin) NumInput() int { return -1 }

func (s *mockStmtAdmin) Exec(args []driver.Value) (driver.Result, error) {
	return &mockResultAdmin{}, nil
}

func (s *mockStmtAdmin) Query(args []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "FROM users WHERE id=") || strings.Contains(s.query, "FROM users WHERE id=$") {
		return &mockRowsAdmin{
			columns: []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"},
			data: [][]driver.Value{
				{int64(1), "admin", "admin@test.com", models.RoleAdmin, true, false, false, time.Now()},
			},
		}, nil
	}
	if strings.Contains(s.query, "INSERT INTO users") {
		return &mockRowsAdmin{
			columns: []string{"id"},
			data: [][]driver.Value{
				{int64(1)},
			},
		}, nil
	}
	return &mockRowsAdmin{}, nil
}

type mockResultAdmin struct{}

func (r *mockResultAdmin) LastInsertId() (int64, error) { return 1, nil }
func (r *mockResultAdmin) RowsAffected() (int64, error) { return 1, nil }

type mockRowsAdmin struct {
	columns []string
	data    [][]driver.Value
	index   int
}

func (r *mockRowsAdmin) Columns() []string {
	return r.columns
}

func (r *mockRowsAdmin) Close() error {
	return nil
}

func (r *mockRowsAdmin) Next(dest []driver.Value) error {
	if r.index >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.index])
	r.index++
	return nil
}

func init() {
	sql.Register("mock_driver_admin", &mockDriverAdmin{})
}

func setupTestServerAdmin(t *testing.T) *Server {
	db, err := sql.Open("mock_driver_admin", "ignored")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-secret-at-least-16")
	cfg := &config.Config{
		SessionSecret: "test-secret-at-least-16",
	}
	return &Server{
		cfg:   cfg,
		store: st,
	}
}

func TestAdminValidation_CreateUser(t *testing.T) {
	s := setupTestServerAdmin(t)

	cases := []struct {
		name         string
		username     string
		password     string
		role         string
		wantRedirect string
		wantFlash    string
	}{
		{
			name:         "empty username",
			username:     "",
			password:     "validPass123",
			role:         models.RoleEditor,
			wantRedirect: "/admin/users",
			wantFlash:    "Benutzername ist erforderlich.",
		},
		{
			name:         "too long username",
			username:     strings.Repeat("a", 101),
			password:     "validPass123",
			role:         models.RoleEditor,
			wantRedirect: "/admin/users",
			wantFlash:    "Benutzername darf maximal 100 Zeichen lang sein.",
		},
		{
			name:         "valid creation",
			username:     "validUser1",
			password:     "validPass123",
			role:         models.RoleEditor,
			wantRedirect: "/admin/users",
			wantFlash:    "Benutzer angelegt.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("username", tc.username)
			form.Set("password", tc.password)
			form.Set("role", tc.role)

			req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			rr := httptest.NewRecorder()
			s.handleUserCreate(rr, req)

			if rr.Code != http.StatusSeeOther {
				t.Fatalf("expected redirect (303), got %d", rr.Code)
			}

			loc := rr.Header().Get("Location")
			if loc != tc.wantRedirect {
				t.Errorf("expected redirect to %s, got %s", tc.wantRedirect, loc)
			}

			// Copy cookies from Response to Request so readFlash can read it
			for _, cookie := range rr.Result().Cookies() {
				req.AddCookie(cookie)
			}

			// Verify flash cookie content
			flashMsg, flashKind := s.readFlash(rr, req)
			if flashMsg != tc.wantFlash {
				t.Errorf("expected flash message %q, got %q (kind: %q)", tc.wantFlash, flashMsg, flashKind)
			}
		})
	}
}

func TestAdminValidation_UpdateUser(t *testing.T) {
	s := setupTestServerAdmin(t)

	cases := []struct {
		name         string
		username     string
		email        string
		wantRedirect string
		wantFlash    string
	}{
		{
			name:         "empty username",
			username:     "",
			email:        "test@test.com",
			wantRedirect: "/admin/users",
			wantFlash:    "Benutzername darf nicht leer sein.",
		},
		{
			name:         "too long username",
			username:     strings.Repeat("a", 101),
			email:        "test@test.com",
			wantRedirect: "/admin/users",
			wantFlash:    "Benutzername darf maximal 100 Zeichen lang sein.",
		},
		{
			name:         "too long email",
			username:     "validUser",
			email:        strings.Repeat("a", 246) + "@test.com", // 246 + 9 = 255 chars
			wantRedirect: "/admin/users",
			wantFlash:    "E‑Mail‑Adresse darf maximal 254 Zeichen lang sein.",
		},
		{
			name:         "invalid email format",
			username:     "validUser",
			email:        "invalid-email",
			wantRedirect: "/admin/users",
			wantFlash:    "Ungültige E‑Mail‑Adresse.",
		},
		{
			name:         "valid update",
			username:     "updatedUser",
			email:        "new@test.com",
			wantRedirect: "/admin/users",
			wantFlash:    "Zugangsdaten aktualisiert.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("username", tc.username)
			form.Set("email", tc.email)

			req := httptest.NewRequest(http.MethodPost, "/admin/users/1/update", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "1")

			ctx := req.Context()
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			s.handleUserUpdate(rr, req)

			if rr.Code != http.StatusSeeOther {
				t.Fatalf("expected redirect (303), got %d", rr.Code)
			}

			loc := rr.Header().Get("Location")
			if loc != tc.wantRedirect {
				t.Errorf("expected redirect to %s, got %s", tc.wantRedirect, loc)
			}

			// Copy cookies from Response to Request so readFlash can read it
			for _, cookie := range rr.Result().Cookies() {
				req.AddCookie(cookie)
			}

			// Verify flash cookie content
			flashMsg, flashKind := s.readFlash(rr, req)
			if flashMsg != tc.wantFlash {
				t.Errorf("expected flash message %q, got %q (kind: %q)", tc.wantFlash, flashMsg, flashKind)
			}
		})
	}
}
