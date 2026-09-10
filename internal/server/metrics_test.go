package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/store"
)

func TestHandleMetricsBackupStatus(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failure := false
	for _, tc := range []struct {
		name    string
		when    time.Time
		wantAge bool
	}{
		{name: "missing timestamp"},
		{name: "future timestamp", when: time.Now().Add(time.Hour)},
		{name: "valid timestamp", when: time.Now().Add(-time.Hour), wantAge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "status.json")
			data, err := json.Marshal(backup.Status{LastBackup: tc.when, RestoreTested: tc.when, S3OK: &failure})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			s := &Server{cfg: &config.Config{MetricsToken: "tok", BackupStatusFile: path},
				store: store.New(db, "k"), started: time.Now()}
			r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			r.Header.Set("Authorization", "Bearer tok")
			w := httptest.NewRecorder()
			s.handleMetrics(w, r)
			body := w.Body.String()
			if !strings.Contains(body, "\ntreckrr_backup_s3_ok 0\n") {
				t.Error("missing failed S3 gauge")
			}
			for _, metric := range []string{"treckrr_backup_age_seconds", "treckrr_backup_restore_tested_age_seconds"} {
				present := false
				for _, line := range strings.Split(body, "\n") {
					if raw, ok := strings.CutPrefix(line, metric+" "); ok {
						present = true
						age, err := strconv.ParseFloat(raw, 64)
						if err != nil || age < 0 || age > 7200 {
							t.Errorf("invalid age metric %s: %s", metric, raw)
						}
					}
				}
				if present != tc.wantAge {
					t.Errorf("%s present = %v, want %v", metric, present, tc.wantAge)
				}
			}
		})
	}
}

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
