package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

func TestMetricsAuthorized(t *testing.T) {
	s := &Server{cfg: &config.Config{MetricsToken: "s3cret-token"}}
	cases := []struct {
		name, header string
		want         bool
	}{
		{"no header", "", false},
		{"wrong scheme", "Basic s3cret-token", false},
		{"wrong token", "Bearer nope", false},
		{"correct", "Bearer s3cret-token", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := s.metricsAuthorized(r); got != c.want {
			t.Errorf("%s: metricsAuthorized = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHandleMetrics(t *testing.T) {
	// sql.Open is lazy — no connection is made, so DBStats() returns zeroed pool
	// stats and the handler renders without a live database.
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/none")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	s := &Server{cfg: &config.Config{MetricsToken: "tok"}, store: store.New(db, "k"), started: time.Now()}

	// Unauthorized → 401.
	w := httptest.NewRecorder()
	s.handleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status = %d, want 401", w.Code)
	}

	// Authorized → 200 with the expected exposition lines.
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer tok")
	s.handleMetrics(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("authorized: status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"treckrr_build_info", "treckrr_uptime_seconds", "go_goroutines", "treckrr_db_connections_open"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing metric %q", want)
		}
	}
}
