package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/backup"
	"treckrr/internal/config"
	"treckrr/internal/db"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

// withSearchPath appends a Postgres search_path runtime parameter to a DSN so
// every pooled connection resolves unqualified names in the isolated schema
// first (public second, so shared extensions/functions still resolve).
func withSearchPath(base, schema string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// TestServerExportsIntegration drives the real HTTP handlers against a real
// Postgres: it logs in through the actual login+CSRF flow, seeds a neighbor with
// two bookings via the store, then exercises the authenticated export endpoints
// (CSV per neighbor and the DSGVO JSON export) plus the dashboard. It runs only
// when TEST_DATABASE_URL is set.
func TestServerExportsIntegration(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()

	// Run in a private schema so this test's admin/neighbor rows never share the
	// global users table with the store integration tests (one of which asserts on
	// the global admin count). Named per-PID so parallel package binaries don't
	// collide; dropped on the way out.
	schema := fmt.Sprintf("srv_itest_%d", os.Getpid())
	boot, err := db.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect (bootstrap): %v", err)
	}
	if _, err := boot.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	boot.Close()
	t.Cleanup(func() {
		b, err := db.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer b.Close()
		_, _ = b.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	pool, err := db.Connect(ctx, withSearchPath(base, schema))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret-32-bytes-long")

	const adminUser, adminPass = "itadmin", "integration-pass-123"
	if err := st.EnsureAdmin(ctx, adminUser, adminPass, false); err != nil {
		t.Fatalf("ensure admin: %v", err)
	}
	// Fresh admins are flagged must-change-password; clear it so the session can
	// reach the export routes without being bounced to the password page.
	admin, err := st.AuthenticateUser(ctx, adminUser, adminPass)
	if err != nil {
		t.Fatalf("authenticate admin: %v", err)
	}
	if err := st.SetMustChangePassword(ctx, admin.ID, false); err != nil {
		t.Fatalf("clear must-change: %v", err)
	}

	cfg := &config.Config{
		SessionSecret:    "test-session-secret-at-least-32-bytes!!",
		EncryptionSecret: "test-session-secret-at-least-32-bytes!!",
		AdminUsername:    adminUser,
		RPID:             "localhost",
		RPOrigin:         "http://localhost",
	}
	srv, err := New(cfg, st, backup.New(backup.Options{}, pool))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		// Don't follow redirects automatically: several handlers 303 on success and
		// the test asserts on the redirect itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginViaHTTP(t, client, ts.URL, adminUser, adminPass)

	// Seed one neighbor in a fresh billing year with two bookings (90.00 + 128.00).
	yr := 2099
	baseID, err := st.CreateEmptyBase(ctx, yr, "IT-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "IT-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "IT Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor to year: %v", err)
	}
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Date(yr, 5, 9, 0, 0, 0, 0, time.UTC),
		TaskLabel: "Mähen", Unit: "h", Hours: decimal.RequireFromString("2.25"),
		HourlyRate: decimal.RequireFromString("40"), Cost: decimal.RequireFromString("90.00"),
	}, nil); err != nil {
		t.Fatalf("entry1: %v", err)
	}
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Date(yr, 9, 14, 0, 0, 0, 0, time.UTC),
		TaskLabel: "Ballenpressen", Unit: "Ballen", Quantity: decimal.RequireFromString("40"),
		UnitPrice: decimal.RequireFromString("3.20"), Cost: decimal.RequireFromString("128.00"),
	}, nil); err != nil {
		t.Fatalf("entry2: %v", err)
	}

	// Dashboard renders for the authenticated admin.
	if code, _ := clientGet(t, client, ts.URL+"/"); code != http.StatusOK {
		t.Errorf("dashboard: status = %d, want 200", code)
	}

	// CSV export for the neighbor lists the two bookings and their German total.
	code, csv := clientGet(t, client, ts.URL+"/export/neighbor/"+strconv.FormatInt(nid, 10)+"?year="+strconv.FormatInt(yearID, 10))
	if code != http.StatusOK {
		t.Fatalf("csv export: status = %d, want 200", code)
	}
	for _, want := range []string{"IT Nachbar", "Mähen", "Ballenpressen", "90,00", "128,00", "218,00"} {
		if !strings.Contains(csv, want) {
			t.Errorf("csv export missing %q", want)
		}
	}

	// DSGVO export returns valid JSON with the subject and both bookings.
	code, body := clientGet(t, client, ts.URL+"/neighbors/"+strconv.FormatInt(nid, 10)+"/dsgvo-export.json")
	if code != http.StatusOK {
		t.Fatalf("dsgvo export: status = %d, want 200", code)
	}
	var exp dsgvoExport
	if err := json.Unmarshal([]byte(body), &exp); err != nil {
		t.Fatalf("dsgvo export not valid JSON: %v", err)
	}
	if exp.Subject.Name != "IT Nachbar" {
		t.Errorf("dsgvo subject = %q, want %q", exp.Subject.Name, "IT Nachbar")
	}
	entries := 0
	for _, y := range exp.BillingYears {
		entries += len(y.Entries)
	}
	if entries != 2 {
		t.Errorf("dsgvo export entries = %d, want 2", entries)
	}
}

var csrfFieldRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

// loginViaHTTP performs the real double-submit login: GET /login to seed the
// login-CSRF cookie and read the token, then POST the credentials. It fails the
// test if the login does not result in a session (a redirect away from /login).
func loginViaHTTP(t *testing.T, client *http.Client, base, user, pass string) {
	t.Helper()
	_, html := clientGet(t, client, base+"/login")
	m := csrfFieldRe.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("login page: csrf_token field not found")
	}
	form := url.Values{"username": {user}, "password": {pass}, "csrf_token": {m[1]}}
	resp, err := client.PostForm(base+"/login", form)
	if err != nil {
		t.Fatalf("post login: %v", err)
	}
	defer resp.Body.Close()
	// Success redirects (303) to the dashboard; a 200 means the login form was
	// re-rendered with an error.
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("login failed: status = %d, want a redirect", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "/login") {
		t.Fatalf("login bounced back to %q", loc)
	}
}

func clientGet(t *testing.T, client *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := client.Get(u) //nolint:gosec // u is the test's own httptest server URL

	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
