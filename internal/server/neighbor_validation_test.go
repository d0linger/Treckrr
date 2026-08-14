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

type mockNeighborDriver struct{}

func (d *mockNeighborDriver) Open(name string) (driver.Conn, error) {
	return &mockNeighborConn{}, nil
}

type mockNeighborConn struct{}

func (c *mockNeighborConn) Prepare(query string) (driver.Stmt, error) {
	return &mockNeighborStmt{query: query}, nil
}
func (c *mockNeighborConn) Close() error              { return nil }
func (c *mockNeighborConn) Begin() (driver.Tx, error) { return &mockNeighborTx{}, nil }

type mockNeighborTx struct{}

func (t *mockNeighborTx) Commit() error   { return nil }
func (t *mockNeighborTx) Rollback() error { return nil }

type mockNeighborStmt struct {
	query string
}

func (s *mockNeighborStmt) Close() error  { return nil }
func (s *mockNeighborStmt) NumInput() int { return -1 }
func (s *mockNeighborStmt) Exec(args []driver.Value) (driver.Result, error) {
	return &mockNeighborResult{}, nil
}
func (s *mockNeighborStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockNeighborRows{query: s.query}, nil
}

type mockNeighborResult struct{}

func (r *mockNeighborResult) LastInsertId() (int64, error) { return 1, nil }
func (r *mockNeighborResult) RowsAffected() (int64, error) { return 1, nil }

type mockNeighborRows struct {
	query   string
	hasRead bool
}

func (r *mockNeighborRows) Columns() []string {
	if strings.Contains(r.query, "INSERT INTO neighbors") {
		return []string{"id"}
	}
	// GetNeighbor columns: id, name, note, archived, created_at
	return []string{"id", "name", "note", "archived", "created_at"}
}

func (r *mockNeighborRows) Close() error { return nil }

func (r *mockNeighborRows) Next(dest []driver.Value) error {
	if r.hasRead {
		return io.EOF
	}
	r.hasRead = true
	if strings.Contains(r.query, "INSERT INTO") {
		dest[0] = int64(123)
	} else {
		// GetNeighbor expectation
		dest[0] = int64(456)
		dest[1] = "Old Neighbor"
		dest[2] = "Old Note"
		dest[3] = false
		dest[4] = time.Now()
	}
	return nil
}

func init() {
	sql.Register("mock_neighbor", &mockNeighborDriver{})
}

func testNeighborServer(t *testing.T) *Server {
	db, err := sql.Open("mock_neighbor", "")
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

func TestHandleNeighborCreateValidation(t *testing.T) {
	s := testNeighborServer(t)

	t.Run("empty name rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "")
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleNeighborManageCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Name darf nicht leer sein.")) {
			t.Errorf("expected empty name flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long name rejected", func(t *testing.T) {
		longName := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("name", longName)
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleNeighborManageCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Name darf höchstens 100 Zeichen lang sein.")) {
			t.Errorf("expected long name flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long note rejected", func(t *testing.T) {
		longNote := strings.Repeat("n", 501)
		form := url.Values{}
		form.Set("name", "Good Neighbor")
		form.Set("note", longNote)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleNeighborManageCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Notiz darf höchstens 500 Zeichen lang sein.")) {
			t.Errorf("expected long note flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("boundary values accepted", func(t *testing.T) {
		maxName := strings.Repeat("a", 100)
		maxNote := strings.Repeat("n", 500)
		form := url.Values{}
		form.Set("name", maxName)
		form.Set("note", maxNote)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleNeighborManageCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Nachbar angelegt.")) {
			t.Errorf("expected success flash for boundary values, got cookie: %q", flashCookie)
		}
	})

	t.Run("valid input accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Good Neighbor")
		form.Set("note", "Good Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/create", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		s.handleNeighborManageCreate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Nachbar angelegt.")) {
			t.Errorf("expected success flash message, got cookie: %q", flashCookie)
		}
	})
}

func TestHandleNeighborUpdateValidation(t *testing.T) {
	s := testNeighborServer(t)

	t.Run("empty name rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "")
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/456/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "456")
		rr := httptest.NewRecorder()

		s.handleNeighborUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Name darf nicht leer sein.")) {
			t.Errorf("expected empty name flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long name rejected", func(t *testing.T) {
		longName := strings.Repeat("a", 101)
		form := url.Values{}
		form.Set("name", longName)
		form.Set("note", "Valid Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/456/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "456")
		rr := httptest.NewRecorder()

		s.handleNeighborUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Name darf höchstens 100 Zeichen lang sein.")) {
			t.Errorf("expected long name flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long note rejected", func(t *testing.T) {
		longNote := strings.Repeat("n", 501)
		form := url.Values{}
		form.Set("name", "Good Neighbor")
		form.Set("note", longNote)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/456/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "456")
		rr := httptest.NewRecorder()

		s.handleNeighborUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Notiz darf höchstens 500 Zeichen lang sein.")) {
			t.Errorf("expected long note flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("boundary values accepted", func(t *testing.T) {
		maxName := strings.Repeat("a", 100)
		maxNote := strings.Repeat("n", 500)
		form := url.Values{}
		form.Set("name", maxName)
		form.Set("note", maxNote)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/456/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "456")
		rr := httptest.NewRecorder()

		s.handleNeighborUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Nachbar aktualisiert.")) {
			t.Errorf("expected success flash for boundary values, got cookie: %q", flashCookie)
		}
	})

	t.Run("valid input accepted", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Good Neighbor")
		form.Set("note", "Good Note")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/456/update", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "456")
		rr := httptest.NewRecorder()

		s.handleNeighborUpdate(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(flashCookie, url.QueryEscape("Nachbar aktualisiert.")) {
			t.Errorf("expected success flash message, got cookie: %q", flashCookie)
		}
	})
}
