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

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

var mockEntryVoided = false

type mockRecurDriver struct{}

func (d *mockRecurDriver) Open(name string) (driver.Conn, error) {
	return &mockRecurConn{}, nil
}

type mockRecurConn struct{}

func (c *mockRecurConn) Prepare(query string) (driver.Stmt, error) {
	return &mockRecurStmt{query: query}, nil
}
func (c *mockRecurConn) Close() error              { return nil }
func (c *mockRecurConn) Begin() (driver.Tx, error) { return &mockRecurTx{}, nil }

type mockRecurTx struct{}

func (t *mockRecurTx) Commit() error   { return nil }
func (t *mockRecurTx) Rollback() error { return nil }

type mockRecurStmt struct {
	query string
}

func (s *mockRecurStmt) Close() error  { return nil }
func (s *mockRecurStmt) NumInput() int { return -1 }
func (s *mockRecurStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockRecurResult{}, nil
}

func (s *mockRecurStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockRecurRows{query: s.query}, nil
}

type mockRecurResult struct{}

func (r *mockRecurResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockRecurResult) RowsAffected() (int64, error) { return 1, nil }

type mockRecurRows struct {
	query   string
	hasRead bool
}

func (r *mockRecurRows) Columns() []string {
	if strings.Contains(r.query, "SELECT machine_id FROM entry_machines") {
		return []string{"machine_id"}
	}
	return []string{
		"id", "neighbor_id", "billing_year_id", "entry_date", "task_label",
		"gespann_id", "tractor_id", "load_level_id", "tractor_label", "load_label",
		"machine_labels", "hours", "hourly_rate", "cost", "note",
		"voided", "void_reason", "created_at", "unit", "quantity", "unit_price",
	}
}

func (r *mockRecurRows) Close() error { return nil }

func (r *mockRecurRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "SELECT machine_id FROM entry_machines") {
		return io.EOF
	}
	// GetEntry return
	dest[0] = int64(10)                           // id
	dest[1] = int64(2)                            // neighbor_id
	dest[2] = int64(1)                            // billing_year_id
	dest[3] = time.Now()                          // entry_date
	dest[4] = "Mähen"                             // task_label
	dest[5] = nil                                 // gespann_id
	dest[6] = nil                                 // tractor_id
	dest[7] = nil                                 // load_level_id
	dest[8] = ""                                  // tractor_label
	dest[9] = ""                                  // load_label
	dest[10] = ""                                 // machine_labels
	dest[11] = "2"                                // hours
	dest[12] = "50"                               // hourly_rate
	dest[13] = "100"                              // cost
	dest[14] = "Note"                             // note
	dest[15] = mockEntryVoided                    // voided
	dest[16] = ""                                 // void_reason
	dest[17] = time.Now()                         // created_at
	dest[18] = "h"                                // unit
	dest[19] = "2"                                // quantity
	dest[20] = "50"                               // unit_price
	return nil
}

func init() {
	sql.Register("mock_recur", &mockRecurDriver{})
}

func testRecurServer(t *testing.T) *Server {
	db, err := sql.Open("mock_recur", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
	}
	return &Server{
		cfg:   cfg,
		store: st,
	}
}

func TestHandleRecurringCreateSecurity(t *testing.T) {
	s := testRecurServer(t)

	t.Run("voided entry rejected for recurring rule", func(t *testing.T) {
		mockEntryVoided = true
		form := url.Values{}
		form.Set("interval_kind", "weekly")
		form.Set("next_run", "2026-08-01")

		req := httptest.NewRequest(http.MethodPost, "/entries/10/recur", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "10")
		rr := httptest.NewRecorder()

		s.handleRecurringCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Stornierte Buchungen können nicht als wiederkehrende Buchung eingerichtet werden.") {
			t.Errorf("expected voided entry error flash, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long next_run rejected", func(t *testing.T) {
		mockEntryVoided = false
		form := url.Values{}
		form.Set("interval_kind", "weekly")
		form.Set("next_run", strings.Repeat("2026-01-01-", 15)) // > 100 chars

		req := httptest.NewRequest(http.MethodPost, "/entries/10/recur", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "10")
		rr := httptest.NewRecorder()

		s.handleRecurringCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Nächstes Datum darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected long next_run error flash, got cookie: %q", flashCookie)
		}
	})
}
