package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

func TestFeedbackResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
	}{
		{name: "confirmed", input: "confirmed", want: "confirmed"},
		{name: "disputed", input: "disputed", want: "disputed"},
		{name: "unknown", input: "invalid"},
		{name: "oversized", input: strings.Repeat("x", 1000)},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?feedback="+url.QueryEscape(tc.input), nil)
			if got := feedbackResult(r); got != tc.want {
				t.Fatalf("feedback = %q, want %q", got, tc.want)
			}
		})
	}
}

// portalValidationDB permits only the limiter and share lookups. Invoice reads
// return no rows, so the tests can distinguish validation from persistence.
type portalValidationDB struct {
	limiterReads int
	shareReads   int
	invoiceReads int
}

func (d *portalValidationDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *portalValidationDB) Driver() driver.Driver                        { return d }
func (d *portalValidationDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *portalValidationDB) Close() error                                 { return nil }
func (d *portalValidationDB) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepared statement")
}
func (d *portalValidationDB) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("unexpected transaction")
}

func (d *portalValidationDB) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "FROM login_attempts"):
		d.limiterReads++
		return &portalValidationRows{values: []driver.Value{false}}, nil
	case strings.Contains(query, "FROM beleg_shares"):
		d.shareReads++
		if len(args) != 1 || args[0].Value != store.HashToken("test-portal-token") {
			return nil, fmt.Errorf("unexpected share credential")
		}
		return &portalValidationRows{values: []driver.Value{int64(1), int64(2), int64(3), int64(4), true}}, nil
	case strings.Contains(query, "FROM invoices"):
		d.invoiceReads++
		return &portalValidationRows{}, nil
	default:
		return nil, fmt.Errorf("unexpected portal query")
	}
}

type portalValidationRows struct {
	values []driver.Value
}

func (r *portalValidationRows) Columns() []string { return make([]string, len(r.values)) }
func (r *portalValidationRows) Close() error      { return nil }
func (r *portalValidationRows) Next(dest []driver.Value) error {
	// A nil value slice represents an exhausted or empty database result.
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func TestPortalFeedbackInputValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		status       string
		message      string
		line         string
		wantError    string
		wantInvoices int
	}{
		{
			name: "oversized ASCII message", status: "disputed",
			message: strings.Repeat("a", maxNoteLen+1), wantError: "Die Nachricht ist zu lang.",
		},
		{
			name: "oversized Unicode message", status: "disputed",
			message: strings.Repeat("ä", maxNoteLen+1), wantError: "Die Nachricht ist zu lang.",
		},
		{
			name: "Unicode message at limit", status: "disputed",
			message: strings.Repeat("ä", maxNoteLen), wantInvoices: 1,
		},
		{
			name: "trim before counting", status: "disputed",
			message: " \t" + strings.Repeat("ä", maxNoteLen) + "\n", wantInvoices: 1,
		},
		{
			name: "oversized line", status: "disputed", message: "Einwand",
			line: strings.Repeat("1", maxNameLen+1), wantError: "Ungültige Rechnungsposition.",
		},
		{
			name: "Unicode line over limit", status: "disputed", message: "Einwand",
			line: strings.Repeat("ä", maxNameLen+1), wantError: "Ungültige Rechnungsposition.",
		},
		{
			name: "invalid status", status: "other", message: "Einwand",
			wantError: "Ungültige Rückmeldung.",
		},
		{
			name: "empty dispute", status: "disputed", message: " \t\n",
			wantError: "Bitte den Einwand kurz beschreiben.",
		},
		{name: "confirmation", status: "confirmed", wantInvoices: 1},
		{
			name: "line at limit", status: "disputed", message: "Einwand",
			line: strings.Repeat("0", maxNameLen), wantInvoices: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := &portalValidationDB{}
			db := sql.OpenDB(fixture)
			t.Cleanup(func() { _ = db.Close() })
			st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
			s := &Server{cfg: &config.Config{}, store: st, logins: newLoginLimiter(st)}
			form := url.Values{"status": {tc.status}, "message": {tc.message}, "line": {tc.line}}
			req := httptest.NewRequest(http.MethodPost,
				"/s/portal/test-portal-token/feedback", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("token", "test-portal-token")
			rr := httptest.NewRecorder()
			s.handlePortalFeedback(rr, req)
			wantStatus := http.StatusBadRequest
			if tc.wantError == "" {
				wantStatus = http.StatusNotFound // Valid input reaches the absent invoice.
			}
			if rr.Code != wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, wantStatus, rr.Body.String())
			}
			if tc.wantError != "" && !strings.Contains(rr.Body.String(), tc.wantError) {
				t.Errorf("body lacks validation error %q", tc.wantError)
			}
			if fixture.invoiceReads != tc.wantInvoices {
				t.Errorf("invoice queries = %d, want %d", fixture.invoiceReads, tc.wantInvoices)
			}
			if fixture.limiterReads != 1 || fixture.shareReads != 1 {
				t.Errorf("limiter/share queries = %d/%d, want 1/1", fixture.limiterReads, fixture.shareReads)
			}
		})
	}
}
