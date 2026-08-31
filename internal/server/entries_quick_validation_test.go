package server

import (
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

// The point of this mock is not to answer queries but to COUNT them. The cap on
// Schnellerfassung rows exists because each row costs five round trips, so the
// guard is only worth anything if it runs before the first one; a mock that
// happily served results would hide a guard placed too late.
type countingDriver struct{ n *int }

func (d countingDriver) Open(string) (driver.Conn, error) { return countingConn(d), nil }

type countingConn struct{ n *int }

func (c countingConn) Prepare(string) (driver.Stmt, error) {
	*c.n++
	return nil, driver.ErrSkip
}
func (c countingConn) Close() error              { return nil }
func (c countingConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

var quickDriverSeq atomic.Uint64

func quickServer(t *testing.T) (*Server, *int) {
	t.Helper()
	n := 0
	// sql.Register panics on a duplicate name, and a name derived from the test
	// repeats as soon as the same test runs twice in one process — `go test
	// -count=2` is enough. A process-wide counter keeps every registration
	// distinct.
	name := "mock_quick_" + strconv.FormatUint(quickDriverSeq.Add(1), 10)
	sql.Register(name, countingDriver{&n})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open mock db: %v", err)
	}
	st := store.New(db, "test-encryption-key-at-least-32-bytes!!")
	return &Server{
		cfg:    &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"},
		store:  st,
		logins: newLoginLimiter(st),
	}, &n
}

func quickForm(rows int) url.Values {
	form := url.Values{}
	form.Set("neighbor_id", "7")
	form.Set("year_id", "3")
	for i := 0; i < rows; i++ {
		form.Add("q_gespann", strconv.Itoa(i+1))
		form.Add("q_hours", "1,5")
		form.Add("q_date", "2026-01-01")
	}
	return form
}

func postQuick(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/entries/quick", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.handleQuickEntries(rr, req)
	return rr
}

func TestQuickEntriesOverLimitIsRejectedBeforeAnyQuery(t *testing.T) {
	s, queries := quickServer(t)

	rr := postQuick(t, s, quickForm(maxQuickEntries+1))

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc, want := rr.Header().Get("Location"), neighborURL(7, 3); loc != want {
		t.Errorf("Location = %q, want %q", loc, want)
	}
	// Rejected, not truncated: the caller is told, rather than being shown a
	// success message for a subset of the rows they submitted.
	if msg := flashText(t, s, rr); !strings.Contains(msg, "Zu viele Zeilen") {
		t.Errorf("flash = %q, want the over-limit message", msg)
	}
	// The whole point of the guard: an abusive submit must not reach the store.
	if *queries != 0 {
		t.Errorf("%d database round trips for a rejected request, want 0 — the guard runs too late", *queries)
	}
}

func TestQuickEntriesAtLimitIsNotRejected(t *testing.T) {
	s, queries := quickServer(t)

	rr := postQuick(t, s, quickForm(maxQuickEntries))

	if msg := flashText(t, s, rr); strings.Contains(msg, "Zu viele Zeilen") {
		t.Errorf("exactly %d rows was rejected; the cap is off by one", maxQuickEntries)
	}
	// It got past the guard and started loading the billing year, which is what
	// distinguishes "allowed through" from "rejected" without needing the mock to
	// serve a full ceremony.
	if *queries == 0 {
		t.Error("no query issued for an allowed request — it never reached the store")
	}
}
