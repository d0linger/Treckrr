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

type mockQuickDriver struct{}

func (d *mockQuickDriver) Open(name string) (driver.Conn, error) {
	return &mockQuickConn{}, nil
}

type mockQuickConn struct{}

func (c *mockQuickConn) Prepare(query string) (driver.Stmt, error) {
	return &mockQuickStmt{query: query}, nil
}
func (c *mockQuickConn) Close() error              { return nil }
func (c *mockQuickConn) Begin() (driver.Tx, error) { return &mockQuickTx{}, nil }

type mockQuickTx struct{}

func (t *mockQuickTx) Commit() error   { return nil }
func (t *mockQuickTx) Rollback() error { return nil }

type mockQuickStmt struct {
	query string
}

func (s *mockQuickStmt) Close() error  { return nil }
func (s *mockQuickStmt) NumInput() int { return -1 }

var createdEntryCount int32

func (s *mockQuickStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockQuickResult{}, nil
}
func (s *mockQuickStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockQuickRows{query: s.query}, nil
}

type mockQuickResult struct{}

func (r *mockQuickResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockQuickResult) RowsAffected() (int64, error) { return 1, nil }

type mockQuickRows struct {
	query   string
	readCnt int
}

func (r *mockQuickRows) Columns() []string {
	if strings.Contains(r.query, "FROM billing_years") {
		return []string{"y_id", "y_year", "y_base_id", "y_label", "y_status", "y_created_at", "b_id", "b_year", "b_name", "b_locked", "b_created_at"}
	}
	if strings.Contains(r.query, "FROM billing_year_neighbors") {
		return []string{"exists"}
	}
	if strings.Contains(r.query, "FROM gespanne") {
		return []string{"id", "base_id", "name", "tractor_id", "load_level_id", "sort_order"}
	}
	if strings.Contains(r.query, "FROM tractors") {
		return []string{"id", "base_id", "ident", "name", "ps", "active", "sort_order"}
	}
	if strings.Contains(r.query, "FROM load_levels") {
		return []string{"id", "base_id", "name", "cost_per_ps", "sort_order"}
	}
	if strings.Contains(r.query, "FROM machines") {
		return []string{"id", "price_base_id", "name", "working_width", "cost_per_ab", "category", "sort_order", "active", "created_at"}
	}
	if strings.Contains(r.query, "FROM gespann_machines") {
		return []string{"machine_id"}
	}
	if strings.Contains(r.query, "FROM invoices") {
		return []string{"id"}
	}
	return []string{"id"}
}

func (r *mockQuickRows) Close() error { return nil }

func (r *mockQuickRows) Next(dest []driver.Value) error {
	if strings.Contains(r.query, "INSERT INTO entries") {
		if r.readCnt > 0 {
			return io.EOF
		}
		r.readCnt++
		atomic.AddInt32(&createdEntryCount, 1)
		dest[0] = int64(1)
		return nil
	}
	if strings.Contains(r.query, "FROM gespann_machines") {
		return io.EOF
	}
	if r.readCnt > 0 {
		return io.EOF
	}
	r.readCnt++

	if strings.Contains(r.query, "FROM billing_years") {
		dest[0] = int64(1)
		dest[1] = 2026
		dest[2] = int64(1)
		dest[3] = "2026"
		dest[4] = "in_progress"
		dest[5] = time.Now()
		dest[6] = int64(1)
		dest[7] = 2026
		dest[8] = "Basis 2026"
		dest[9] = false
		dest[10] = time.Now()
		return nil
	}
	if strings.Contains(r.query, "FROM billing_year_neighbors") {
		dest[0] = true
		return nil
	}
	if strings.Contains(r.query, "FROM gespanne") {
		dest[0] = int64(1)
		dest[1] = int64(1)
		dest[2] = "Gespann 1"
		dest[3] = int64(10)
		dest[4] = int64(20)
		dest[5] = 0
		return nil
	}
	if strings.Contains(r.query, "FROM tractors") {
		dest[0] = int64(10)
		dest[1] = int64(1)
		dest[2] = "T1"
		dest[3] = "Traktor 1"
		dest[4] = "100"
		dest[5] = true
		dest[6] = 0
		return nil
	}
	if strings.Contains(r.query, "FROM load_levels") {
		dest[0] = int64(20)
		dest[1] = int64(1)
		dest[2] = "Stufe 1"
		dest[3] = "0.5"
		dest[4] = 0
		return nil
	}
	return io.EOF
}

func init() {
	sql.Register("mock_quick", &mockQuickDriver{})
}

func TestQuickEntriesCappedAtMax(t *testing.T) {
	atomic.StoreInt32(&createdEntryCount, 0)
	db, err := sql.Open("mock_quick", "")
	if err != nil {
		t.Fatalf("failed to open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	cfg := &config.Config{
		SessionSecret: "test-session-secret-at-least-16-bytes",
	}
	s := &Server{
		cfg:   cfg,
		store: st,
	}

	form := url.Values{}
	form.Set("neighbor_id", "1")
	form.Set("year_id", "1")

	// Submit 120 quick entry rows (exceeding maxQuickEntries = 100)
	for i := 0; i < 120; i++ {
		form.Add("q_gespann", "1")
		form.Add("q_hours", "2.5")
		form.Add("q_date", "2026-01-01")
	}

	req := httptest.NewRequest(http.MethodPost, "/entries/quick", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handleQuickEntries(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status SeeOther, got %d", rr.Code)
	}

	count := atomic.LoadInt32(&createdEntryCount)
	if count != maxQuickEntries {
		t.Errorf("expected createdEntryCount to be capped at %d, got %d", maxQuickEntries, count)
	}

	flashCookie := flashText(t, s, rr)
	wantMsg := strconv.Itoa(maxQuickEntries) + " Buchungen gespeichert."
	if !strings.Contains(flashCookie, wantMsg) {
		t.Errorf("expected flash message containing %q, got: %q", wantMsg, flashCookie)
	}
}
