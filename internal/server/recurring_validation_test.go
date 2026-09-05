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

type mockRecurringDriver struct{}

func (d *mockRecurringDriver) Open(name string) (driver.Conn, error) {
	return &mockRecurringConn{}, nil
}

type mockRecurringConn struct{}

func (c *mockRecurringConn) Prepare(query string) (driver.Stmt, error) {
	return &mockRecurringStmt{query: query}, nil
}
func (c *mockRecurringConn) Close() error              { return nil }
func (c *mockRecurringConn) Begin() (driver.Tx, error) { return &mockRecurringTx{}, nil }

type mockRecurringTx struct{}

func (t *mockRecurringTx) Commit() error   { return nil }
func (t *mockRecurringTx) Rollback() error { return nil }

type mockRecurringStmt struct {
	query string
}

func (s *mockRecurringStmt) Close() error  { return nil }
func (s *mockRecurringStmt) NumInput() int { return -1 }
func (s *mockRecurringStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockRecurringResult{}, nil
}

func (s *mockRecurringStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockRecurringRows{query: s.query}, nil
}

type mockRecurringResult struct{}

func (r *mockRecurringResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockRecurringResult) RowsAffected() (int64, error) { return 1, nil }

type mockRecurringRows struct {
	query   string
	hasRead bool
}

func (r *mockRecurringRows) Columns() []string {
	if strings.Contains(r.query, "FROM entries") {
		return []string{
			"id", "neighbor_id", "billing_year_id", "entry_date", "task_label",
			"gespann_id", "tractor_id", "load_level_id", "tractor_label", "load_label",
			"machine_labels", "hours", "hourly_rate", "cost", "note",
			"voided", "void_reason", "created_at", "unit", "quantity", "unit_price",
		}
	}
	return []string{"id"}
}

func (r *mockRecurringRows) Close() error { return nil }

func (r *mockRecurringRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "FROM entries") {
		// Mock GetEntry row matching scanEntry
		dest[0] = int64(1)                          // id
		dest[1] = int64(10)                         // neighbor_id
		dest[2] = int64(100)                        // billing_year_id
		dest[3] = time.Now()                        // entry_date
		dest[4] = "Task Label"                      // task_label
		dest[5] = nil                              // gespann_id
		dest[6] = nil                              // tractor_id
		dest[7] = nil                              // load_level_id
		dest[8] = ""                               // tractor_label
		dest[9] = ""                               // load_label
		dest[10] = ""                              // machine_labels
		dest[11] = "1.0"                           // hours
		dest[12] = "10.0"                          // hourly_rate
		dest[13] = "10.0"                          // cost
		dest[14] = "Note"                          // note
		dest[15] = false                           // voided
		dest[16] = ""                              // void_reason
		dest[17] = time.Now()                       // created_at
		dest[18] = "h"                             // unit
		dest[19] = "1.0"                           // quantity
		dest[20] = "10.0"                          // unit_price
	} else {
		dest[0] = int64(1)
	}
	return nil
}

type mockVoidedRecurringRows struct {
	mockRecurringRows
}

func (r *mockVoidedRecurringRows) Next(dest []driver.Value) error {
	err := r.mockRecurringRows.Next(dest)
	if err == nil && len(dest) > 15 {
		dest[15] = true // voided = true
	}
	return err
}

type mockVoidedRecurringStmt struct {
	mockRecurringStmt
}

func (s *mockVoidedRecurringStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockVoidedRecurringRows{mockRecurringRows{query: s.query}}, nil
}

type mockVoidedRecurringDriver struct {
	mockRecurringDriver
}

func (d *mockVoidedRecurringDriver) Open(name string) (driver.Conn, error) {
	return &mockVoidedRecurringConn{}, nil
}

type mockVoidedRecurringConn struct {
	mockRecurringConn
}

func (c *mockVoidedRecurringConn) Prepare(query string) (driver.Stmt, error) {
	return &mockVoidedRecurringStmt{mockRecurringStmt{query: query}}, nil
}

func init() {
	sql.Register("mock_recurring", &mockRecurringDriver{})
	sql.Register("mock_voided_recurring", &mockVoidedRecurringDriver{})
}

func testRecurringServer(t *testing.T, driverName string) *Server {
	db, err := sql.Open(driverName, "")
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

func TestHandleRecurringCreateValidation(t *testing.T) {
	t.Run("overly long next_run rejected", func(t *testing.T) {
		s := testRecurringServer(t, "mock_recurring")

		longNextRun := strings.Repeat("a", maxNameLen+1)
		form := url.Values{}
		form.Set("next_run", longNextRun)
		form.Set("interval_kind", "weekly")

		req := httptest.NewRequest(http.MethodPost, "/entries/1/recurring", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleRecurringCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Nächste Ausführung darf höchstens 100 Zeichen lang sein.") {
			t.Errorf("expected long next_run flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("voided entry rejected", func(t *testing.T) {
		s := testRecurringServer(t, "mock_voided_recurring")

		form := url.Values{}
		form.Set("next_run", "2026-04-01")
		form.Set("interval_kind", "weekly")

		req := httptest.NewRequest(http.MethodPost, "/entries/1/recurring", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleRecurringCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Aus einer stornierten Buchung kann keine Serie erstellt werden.") {
			t.Errorf("expected voided entry flash message, got cookie: %q", flashCookie)
		}
	})
}
