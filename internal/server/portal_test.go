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

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

type mockPortalDriver struct{}

func (d *mockPortalDriver) Open(name string) (driver.Conn, error) {
	return &mockPortalConn{}, nil
}

type mockPortalConn struct{}

func (c *mockPortalConn) Prepare(query string) (driver.Stmt, error) {
	return &mockPortalStmt{query: query}, nil
}
func (c *mockPortalConn) Close() error              { return nil }
func (c *mockPortalConn) Begin() (driver.Tx, error) { return &mockTx{}, nil }

type mockPortalStmt struct {
	query string
}

func (s *mockPortalStmt) Close() error  { return nil }
func (s *mockPortalStmt) NumInput() int { return -1 }
func (s *mockPortalStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockResult{}, nil
}
func (s *mockPortalStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockPortalRows{query: s.query}, nil
}

type mockPortalRows struct {
	query   string
	hasRead bool
}

func (r *mockPortalRows) Columns() []string {
	if strings.Contains(r.query, "beleg_shares") {
		return []string{"id", "neighbor_id", "billing_year_id", "used_today"}
	}
	return []string{"id"}
}

func (r *mockPortalRows) Close() error { return nil }

func (r *mockPortalRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "beleg_shares") {
		dest[0] = int64(1)
		dest[1] = int64(1)
		dest[2] = int64(1)
		dest[3] = true
	} else {
		dest[0] = int64(1)
	}
	return nil
}

func init() {
	sql.Register("mock_portal", &mockPortalDriver{})
}

// TestPortalFeedbackInputValidation verifies that public portal feedback
// inputs are strictly bounded and reject oversized messages or line inputs.
func TestPortalFeedbackInputValidation(t *testing.T) {
	db, err := sql.Open("mock_portal", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	s := &Server{
		cfg:   &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"},
		store: st,
	}

	for _, tc := range []struct {
		name        string
		status      string
		message     string
		line        string
		wantStatus  int
		wantBodySub string
	}{
		{
			name:        "oversized message",
			status:      "disputed",
			message:     strings.Repeat("a", maxNoteLen+1),
			wantStatus:  http.StatusBadRequest,
			wantBodySub: "Die Nachricht ist zu lang.",
		},
		{
			name:        "oversized line parameter",
			status:      "disputed",
			message:     "Einwand",
			line:        strings.Repeat("1", maxNameLen+1),
			wantStatus:  http.StatusBadRequest,
			wantBodySub: "Ungültige Rechnungsposition.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"status":  {tc.status},
				"message": {tc.message},
			}
			if tc.line != "" {
				form.Set("line", tc.line)
			}
			req := httptest.NewRequest(http.MethodPost, "/s/portal/1/feedback", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("token", "1")

			rr := httptest.NewRecorder()
			s.handlePortalFeedback(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if got := rr.Body.String(); !strings.Contains(got, tc.wantBodySub) {
				t.Errorf("body = %q, want substring %q", got, tc.wantBodySub)
			}
		})
	}
}
