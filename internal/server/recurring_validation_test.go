package server

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

type mockRecurDriver struct{ voided bool }

func (d *mockRecurDriver) Open(name string) (driver.Conn, error) {
	return &mockRecurConn{voided: d.voided}, nil
}

type mockRecurConn struct{ voided bool }

func (c *mockRecurConn) Prepare(query string) (driver.Stmt, error) {
	return &mockRecurStmt{query: query, voided: c.voided}, nil
}
func (c *mockRecurConn) Close() error              { return nil }
func (c *mockRecurConn) Begin() (driver.Tx, error) { return &mockRecurTx{}, nil }

type mockRecurTx struct{}

func (t *mockRecurTx) Commit() error   { return nil }
func (t *mockRecurTx) Rollback() error { return nil }

type mockRecurStmt struct {
	query  string
	voided bool
}

func (s *mockRecurStmt) Close() error  { return nil }
func (s *mockRecurStmt) NumInput() int { return -1 }
func (s *mockRecurStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockRecurResult{}, nil
}
func (s *mockRecurStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockRecurRows{query: s.query, voided: s.voided}, nil
}

type mockRecurResult struct{}

func (r *mockRecurResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockRecurResult) RowsAffected() (int64, error) { return 1, nil }

type mockRecurRows struct {
	query   string
	voided  bool
	hasRead bool
}

func (r *mockRecurRows) Columns() []string {
	if strings.Contains(r.query, "FROM entries") {
		return []string{
			"id", "neighbor_id", "billing_year_id", "entry_date", "task_label",
			"gespann_id", "tractor_id", "load_level_id", "tractor_label",
			"load_label", "machine_labels", "hours", "hourly_rate", "cost",
			"note", "voided", "void_reason", "created_at", "unit", "quantity", "unit_price",
		}
	}
	return []string{"id"}
}

func (r *mockRecurRows) Close() error { return nil }

func (r *mockRecurRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "FROM entries") {
		dest[0] = int64(10)   // id
		dest[1] = int64(1)    // neighbor_id
		dest[2] = int64(1)    // billing_year_id
		dest[3] = time.Now()  // entry_date
		dest[4] = "Mähen"     // task_label
		dest[5] = nil         // gespann_id
		dest[6] = nil         // tractor_id
		dest[7] = nil         // load_level_id
		dest[8] = ""          // tractor_label
		dest[9] = ""          // load_label
		dest[10] = ""         // machine_labels
		dest[11] = "2.00"     // hours
		dest[12] = "30.00"    // hourly_rate
		dest[13] = "60.00"    // cost
		dest[14] = ""         // note
		dest[15] = r.voided   // voided
		dest[16] = ""         // void_reason
		dest[17] = time.Now() // created_at
		dest[18] = "h"        // unit
		dest[19] = "2.00"     // quantity
		dest[20] = "30.00"    // unit_price
		return nil
	}
	return io.EOF
}

var recurDriverSeq atomic.Uint64

func testRecurServer(t *testing.T, voided bool) *Server {
	t.Helper()
	name := "mock_recur_" + strconv.FormatUint(recurDriverSeq.Add(1), 10)
	sql.Register(name, &mockRecurDriver{voided: voided})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	return &Server{
		cfg:    &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"},
		store:  st,
		logins: newLoginLimiter(st),
	}
}

func TestRecurringCreateFromVoidedEntryRejected(t *testing.T) {
	s := testRecurServer(t, true) // entry is voided

	form := url.Values{}
	form.Set("interval_kind", "weekly")
	form.Set("next_run", "2026-06-01")

	req := httptest.NewRequest(http.MethodPost, "/entries/10/recur", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "10")
	rr := httptest.NewRecorder()

	s.handleRecurringCreate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/recurring" {
		t.Errorf("Location = %q, want %q", loc, "/recurring")
	}
	msg := flashText(t, s, rr)
	if !strings.Contains(msg, "Stornierte Buchungen können nicht als Vorlage für wiederkehrende Buchungen verwendet werden.") {
		t.Errorf("flash = %q, want voided entry error message", msg)
	}
}

func TestRecurringCreateOversizedNextRunRejected(t *testing.T) {
	s := testRecurServer(t, false) // entry is not voided

	form := url.Values{}
	form.Set("interval_kind", "weekly")
	form.Set("next_run", strings.Repeat("2026-01-01", maxNameLen))

	req := httptest.NewRequest(http.MethodPost, "/entries/10/recur", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "10")
	rr := httptest.NewRecorder()

	s.handleRecurringCreate(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/recurring" {
		t.Errorf("Location = %q, want %q", loc, "/recurring")
	}
	msg := flashText(t, s, rr)
	if !strings.Contains(msg, "Nächste Ausführung darf höchstens 100 Zeichen lang sein.") {
		t.Errorf("flash = %q, want oversized next_run error message", msg)
	}
}
