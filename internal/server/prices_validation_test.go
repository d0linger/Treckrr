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

type mockPricesDriver struct{}

func (d *mockPricesDriver) Open(name string) (driver.Conn, error) {
	return &mockPricesConn{}, nil
}

type mockPricesConn struct{}

func (c *mockPricesConn) Prepare(query string) (driver.Stmt, error) {
	return &mockPricesStmt{query: query}, nil
}
func (c *mockPricesConn) Close() error              { return nil }
func (c *mockPricesConn) Begin() (driver.Tx, error) { return &mockPricesTx{}, nil }

type mockPricesTx struct{}

func (t *mockPricesTx) Commit() error   { return nil }
func (t *mockPricesTx) Rollback() error { return nil }

type mockPricesStmt struct {
	query string
}

func (s *mockPricesStmt) Close() error  { return nil }
func (s *mockPricesStmt) NumInput() int { return -1 }
func (s *mockPricesStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockPricesResult{}, nil
}

func (s *mockPricesStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockPricesRows{query: s.query}, nil
}

type mockPricesResult struct{}

func (r *mockPricesResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockPricesResult) RowsAffected() (int64, error) { return 1, nil }

type mockPricesRows struct {
	query   string
	hasRead bool
}

func (r *mockPricesRows) Columns() []string {
	if strings.Contains(r.query, "FROM price_bases") {
		return []string{"id", "year", "name", "locked", "created_at"}
	}
	return []string{"id"}
}

func (r *mockPricesRows) Close() error { return nil }

func (r *mockPricesRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "FROM price_bases") {
		dest[0] = int64(1)
		dest[1] = 2026
		dest[2] = "Base 2026"
		dest[3] = false
		dest[4] = time.Now()
	} else {
		dest[0] = int64(1)
	}
	return nil
}

func init() {
	sql.Register("mock_prices", &mockPricesDriver{})
}

func testPricesServer(t *testing.T) *Server {
	db, err := sql.Open("mock_prices", "")
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

func TestHandlePricesValidation(t *testing.T) {
	s := testPricesServer(t)

	t.Run("load level cost_per_ps oversized rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("base_id", "1")
		form.Set("name", "Stufe 1")
		form.Set("cost_per_ps", strings.Repeat("1", maxDecimalLen+1))

		req := httptest.NewRequest(http.MethodPost, "/prices/loads/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleLoadLevelSave(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Kosten je PS darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long cost_per_ps flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("tractor ps oversized rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("base_id", "1")
		form.Set("ident", "T1")
		form.Set("name", "Traktor 1")
		form.Set("ps", strings.Repeat("2", maxDecimalLen+1))

		req := httptest.NewRequest(http.MethodPost, "/prices/tractors/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleTractorSave(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "PS darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long ps flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("machine working_width oversized rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("base_id", "1")
		form.Set("name", "Mähwerk")
		form.Set("working_width", strings.Repeat("3", maxDecimalLen+1))
		form.Set("cost_per_ab", "12.50")

		req := httptest.NewRequest(http.MethodPost, "/prices/machines/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleMachineSave(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Arbeitsbreite darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long working_width flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("machine cost_per_ab oversized rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("base_id", "1")
		form.Set("name", "Mähwerk")
		form.Set("working_width", "3.0")
		form.Set("cost_per_ab", strings.Repeat("4", maxDecimalLen+1))

		req := httptest.NewRequest(http.MethodPost, "/prices/machines/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleMachineSave(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Kosten darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long cost_per_ab flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("machine self_cost_per_h oversized rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("base_id", "1")
		form.Set("name", "Mähwerk")
		form.Set("working_width", "3.0")
		form.Set("cost_per_ab", "12.50")
		form.Set("self_cost_per_h", strings.Repeat("5", maxDecimalLen+1))

		req := httptest.NewRequest(http.MethodPost, "/prices/machines/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleMachineSave(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Selbstkosten darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long self_cost_per_h flash message, got cookie: %q", flashCookie)
		}
	})
}
