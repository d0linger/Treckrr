package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/d0linger/treckrr/internal/store"
)

// closedTestStore produces deterministic database failures without opening any
// network connection, including when the tested refusal writes an audit event.
func closedTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://test:test@127.0.0.1/unused")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return store.New(db, "test-encryption-key-at-least-32-bytes!!")
}

// TestAccountKey pins the normalization of the account-scoped login-limiter key:
// case-folded and trimmed, so failures against "Admin", "admin" and " admin "
// share one throttle bucket.
func TestAccountKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"admin", "pwuser:admin"},
		{"Admin", "pwuser:admin"},
		{"  Bob ", "pwuser:bob"},
		{"MiXeD", "pwuser:mixed"},
		{"", "pwuser:"},
	}
	for _, c := range cases {
		if got := accountKey(c.in); got != c.want {
			t.Errorf("accountKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShareBlockedFailsClosed(t *testing.T) {
	lim := newLoginLimiter(closedTestStore(t))
	if !lim.shareBlocked(t.Context(), "192.0.2.1") {
		t.Fatal("share resolution must be blocked when the limiter cannot be checked")
	}
}

func TestPublicShareResolutionFailsClosed(t *testing.T) {
	s := testServer()
	s.store = closedTestStore(t)
	s.logins = newLoginLimiter(s.store)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "invoice portal", handler: func(w http.ResponseWriter, r *http.Request) { s.resolvePortalShare(w, r) }},
		{name: "statement", handler: func(w http.ResponseWriter, r *http.Request) { s.resolveStatementShare(w, r) }},
		{name: "calendar", handler: s.handleCalendarFeed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/public/test-token", nil)
			req.SetPathValue("token", "test-token")
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 before token resolution", rr.Code)
			}
		})
	}
}
