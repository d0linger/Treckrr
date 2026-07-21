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

func init() {
	sql.Register("mock_admin_driver", &mockAdminDriver{})
}

type mockAdminDriver struct{}

func (d *mockAdminDriver) Open(name string) (driver.Conn, error) {
	return &mockAdminConn{}, nil
}

type mockAdminConn struct{}

func (c *mockAdminConn) Prepare(query string) (driver.Stmt, error) {
	return &mockAdminStmt{query: query}, nil
}

func (c *mockAdminConn) Close() error              { return nil }
func (c *mockAdminConn) Begin() (driver.Tx, error) { return &mockAdminTx{}, nil }

type mockAdminTx struct{}

func (t *mockAdminTx) Commit() error   { return nil }
func (t *mockAdminTx) Rollback() error { return nil }

type mockAdminStmt struct {
	query string
}

func (s *mockAdminStmt) Close() error  { return nil }
func (s *mockAdminStmt) NumInput() int { return -1 }

func (s *mockAdminStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockAdminResult{}, nil
}

func (s *mockAdminStmt) Query(args []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "count(*)") {
		return &mockAdminRows{columns: []string{"count"}, values: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(s.query, "FROM users WHERE id=") {
		return &mockAdminRows{
			columns: []string{"id", "username", "email", "role", "is_admin", "must_change_password", "totp_enabled", "created_at"},
			values: [][]driver.Value{
				{int64(1), "admin", "admin@example.com", "admin", true, false, false, time.Now()},
			},
		}, nil
	}
	if strings.Contains(s.query, "RETURNING id") {
		return &mockAdminRows{columns: []string{"id"}, values: [][]driver.Value{{int64(42)}}}, nil
	}
	return &mockAdminRows{}, nil
}

type mockAdminResult struct{}

func (r *mockAdminResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockAdminResult) RowsAffected() (int64, error) { return 1, nil }

type mockAdminRows struct {
	columns []string
	values  [][]driver.Value
	cursor  int
}

func (r *mockAdminRows) Columns() []string { return r.columns }
func (r *mockAdminRows) Close() error      { return nil }

func (r *mockAdminRows) Next(dest []driver.Value) error {
	if r.cursor >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.cursor])
	r.cursor++
	return nil
}

func TestHandleUserCreateValidation(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "dummy")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	srv := testAdminServer(db)

	adminUser := &models.User{
		ID:       1,
		Username: "admin",
		Role:     models.RoleAdmin,
		IsAdmin:  true,
	}

	tests := []struct {
		name        string
		username    string
		password    string
		role        string
		expectError bool
		expectFlash string
	}{
		{
			name:        "Empty Username",
			username:    "",
			password:    "Pass1234",
			role:        "editor",
			expectError: true,
			expectFlash: "Benutzername ist erforderlich.",
		},
		{
			name:        "Too Long Username",
			username:    strings.Repeat("a", 101),
			password:    "Pass1234",
			role:        "editor",
			expectError: true,
			expectFlash: "Benutzername darf höchstens 100 Zeichen lang sein.",
		},
		{
			name:        "Weak Password",
			username:    "validuser",
			password:    "weak",
			role:        "editor",
			expectError: true,
			expectFlash: "Passwort muss mindestens 8 Zeichen haben.",
		},
		{
			name:        "Valid Input",
			username:    "validuser",
			password:    "Pass1234",
			role:        "editor",
			expectError: false,
			expectFlash: "Benutzer angelegt.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("username", tc.username)
			form.Set("password", tc.password)
			form.Set("role", tc.role)

			req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			srv.handleUserCreate(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
			}

			kind, flash := getFlashMsg(rec)
			if tc.expectError {
				if kind != "error" {
					t.Errorf("expected flash kind 'error', got %q", kind)
				}
			} else {
				if kind != "success" {
					t.Errorf("expected flash kind 'success', got %q", kind)
				}
			}
			if flash != tc.expectFlash {
				t.Errorf("expected flash msg %q, got %q", tc.expectFlash, flash)
			}
		})
	}
}

func TestHandleUserUpdateValidation(t *testing.T) {
	db, err := sql.Open("mock_admin_driver", "dummy")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	defer db.Close()

	srv := testAdminServer(db)

	adminUser := &models.User{
		ID:       1,
		Username: "admin",
		Role:     models.RoleAdmin,
		IsAdmin:  true,
	}

	tests := []struct {
		name        string
		username    string
		email       string
		expectError bool
		expectFlash string
	}{
		{
			name:        "Empty Username",
			username:    "",
			email:       "test@example.com",
			expectError: true,
			expectFlash: "Benutzername darf nicht leer sein.",
		},
		{
			name:        "Too Long Username",
			username:    strings.Repeat("a", 101),
			email:       "test@example.com",
			expectError: true,
			expectFlash: "Benutzername darf höchstens 100 Zeichen lang sein.",
		},
		{
			name:        "Too Long Email",
			username:    "validuser",
			email:       strings.Repeat("a", 255) + "@test.com",
			expectError: true,
			expectFlash: "E-Mail-Adresse darf höchstens 254 Zeichen lang sein.",
		},
		{
			name:        "Valid Input",
			username:    "validuser",
			email:       "test@example.com",
			expectError: false,
			expectFlash: "Zugangsdaten aktualisiert.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("username", tc.username)
			form.Set("email", tc.email)

			req := httptest.NewRequest(http.MethodPost, "/admin/users/1/update", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "1")
			ctx := context.WithValue(req.Context(), userCtxKey, adminUser)
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			srv.handleUserUpdate(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Errorf("expected status %d, got %d", http.StatusSeeOther, rec.Code)
			}

			kind, flash := getFlashMsg(rec)
			if tc.expectError {
				if kind != "error" {
					t.Errorf("expected flash kind 'error', got %q", kind)
				}
			} else {
				if kind != "success" {
					t.Errorf("expected flash kind 'success', got %q", kind)
				}
			}
			if flash != tc.expectFlash {
				t.Errorf("expected flash msg %q, got %q", tc.expectFlash, flash)
			}
		})
	}
}

func testAdminServer(db *sql.DB) *Server {
	cfg := &config.Config{
		SessionSecret: "test-secret-at-least-16",
	}
	st := store.New(db, "test-encryption-secret")
	return &Server{
		cfg:    cfg,
		store:  st,
		logins: newLoginLimiter(st),
	}
}

func getFlashMsg(rec *httptest.ResponseRecorder) (string, string) {
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == flashCookie {
			parts := strings.SplitN(c.Value, "|", 2)
			if len(parts) == 2 {
				decoded, _ := url.QueryUnescape(parts[1])
				return parts[0], decoded
			}
		}
	}
	return "", ""
}
