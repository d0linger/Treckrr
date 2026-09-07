package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// itEnv is a full HTTP server over a real database: config, store, templates,
// the complete middleware chain, and a logged-in admin client. The handler
// families under test here (price master, booking, recalculation, invoice
// lifecycle) had no coverage at all — they were exactly the ones that change
// the numbers every calculation is built on (Ausbaukarte 92-95, 6).
type itEnv struct {
	t      *testing.T
	ctx    context.Context
	pool   *sql.DB
	st     *store.Store
	srv    *httptest.Server
	client *http.Client

	year                 int
	baseID64, yearID64   int64
	neighborID           int64
	tractorID, loadID    int64
	machineID, gespannID int64
	adminPass            string
}

func newItEnv(t *testing.T) *itEnv {
	t.Helper()
	dburl := os.Getenv("TEST_DATABASE_URL")
	if dburl == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dburl)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-key-at-least-32-bytes!!")

	e := &itEnv{t: t, ctx: ctx, pool: pool, st: st, adminPass: "It-Passwort-123!"} //nolint:gosec // G101: fixed password for a throwaway test account
	// Year BAND, not just a unique number. Tests derive their year from the pid,
	// and packages run in parallel with DIFFERENT pids — so two packages whose
	// ranges overlap can land on the same year, and one test's fixture purge then
	// deletes the billing year another test is still using (seen as FK violations
	// on billing_year_id). internal/store spans 3000-5899; this package stays
	// above it with room to spare.
	e.year = 6100 + os.Getpid()%500
	uname := fmt.Sprintf("ithandler%d_%s", os.Getpid(), sanitizeTestName(t.Name()))

	// This env creates an ADMIN user, and TestLastAdminGuardIntegration in the
	// store package asserts a globally-scoped invariant ("is this the last
	// admin?"). `go test ./...` runs packages in parallel against one database, so
	// the two must not overlap. Both take the same session-level advisory lock.
	lockAdminCount(t, ctx, pool)

	// Purge by PREFIX, not by this run's exact name: a crashed earlier run leaves
	// its admin user behind under a different PID, and a stray admin silently
	// breaks the global last-admin invariant the store package asserts. Package
	// tests run sequentially, so a prefix sweep cannot hit a live sibling.
	e.purge("ithandler%")
	e.purge(uname)
	t.Cleanup(func() { e.purge(uname) })

	uid, err := st.CreateUser(ctx, uname, e.adminPass, models.RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	_ = uid

	e.baseID64, err = st.CreateEmptyBase(ctx, e.year, "IT-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	e.yearID64, err = st.CreateBillingYear(ctx, e.year, e.baseID64, "IT-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	e.neighborID, err = st.CreateNeighbor(ctx, "IT-Nachbar "+uname, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	// CreateNeighbor's second argument is the note, not the address — and § 11
	// requires a recipient address before a number may be frozen.
	if err := st.UpdateNeighbor(ctx, e.neighborID, "IT-Nachbar "+uname, "",
		"Feldweg 1, 4710 Testdorf", "", ""); err != nil {
		t.Fatalf("neighbor address: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, e.yearID64, e.neighborID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	e.loadID, err = st.CreateLoadLevel(ctx, e.baseID64, "it-mittel", decimal.RequireFromString("0.36"), 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	e.tractorID, err = st.CreateTractor(ctx, e.baseID64, "IT-T1", "IT-Traktor", decimal.RequireFromString("100"), 1)
	if err != nil {
		t.Fatalf("tractor: %v", err)
	}
	e.machineID, err = st.CreateMachine(ctx, e.baseID64, "IT-Maschine", decimal.RequireFromString("2"), decimal.RequireFromString("5"), "IT", 1)
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	e.gespannID, err = st.CreateGespann(ctx, e.baseID64, "IT-Gespann", &e.tractorID, &e.loadID, []int64{e.machineID}, 1)
	if err != nil {
		t.Fatalf("gespann: %v", err)
	}
	// § 11 UStG: issuing freezes a number only when every mandatory field exists.
	if err := st.UpdateCompany(ctx, models.Company{
		Name: "IT-Betrieb", Address: "Hofstraße 2, 4710 Testdorf", TaxID: "ATU00000000",
		TaxMode: "pauschal", VATRate: decimal.RequireFromString("13"),
	}); err != nil {
		t.Fatalf("company: %v", err)
	}

	cfg := &config.Config{ //nolint:gosec // G101: throwaway secrets for a test server
		SessionSecret: "it-session-secret-at-least-16-bytes",
		RPID:          "localhost",
		RPOrigin:      "http://localhost",
		AdminUsername: uname,
		AdminPassword: e.adminPass,
		BackupKeep:    7,
	}
	srvObj, err := New(cfg, st, backup.New(backup.Options{}, pool))
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	e.srv = httptest.NewServer(srvObj.Handler())
	t.Cleanup(e.srv.Close)

	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar}
	// Every test in this package logs in from 127.0.0.1, and the per-IP login
	// limiter is backed by the shared database — so a run of the whole suite
	// trips it partway through and later tests fail with a misleading "login did
	// not stick". Clear both key spaces for this env's IP and account.
	srvObj.logins.reset(ctx, "127.0.0.1")
	srvObj.logins.accountReset(ctx, uname)
	t.Cleanup(func() {
		srvObj.logins.reset(ctx, "127.0.0.1")
		srvObj.logins.accountReset(ctx, uname)
	})
	// POST /login is guarded by its own seeded token (the session-based csrf
	// middleware cannot cover a request that has no session yet), so the form has
	// to be fetched first: the GET sets the login-CSRF cookie and renders the
	// matching value.
	loginTok := e.csrf("/login")
	resp, err := e.client.PostForm(e.srv.URL+"/login", url.Values{
		"username": {uname}, "password": {e.adminPass}, "csrf_token": {loginTok},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	_ = resp.Body.Close()
	if got := e.get("/"); !strings.Contains(got, "Abmelden") && !strings.Contains(got, "logout") {
		t.Fatalf("login did not stick (dashboard has no logout affordance)")
	}
	return e
}

func sanitizeTestName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// purge removes everything this env creates, children first — 0039 put RESTRICT
// between the financial tables and their parents, so order matters.
func (e *itEnv) purge(uname string) {
	for _, q := range []string{
		`DELETE FROM payments WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM invoices WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM beleg_sends WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM neighbor_ledger WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM entries WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM billing_year_neighbors WHERE billing_year_id IN (SELECT id FROM billing_years WHERE year=$1)`,
		`DELETE FROM billing_years WHERE year=$1`,
		`DELETE FROM price_bases WHERE year=$1`,
	} {
		if _, err := e.pool.ExecContext(e.ctx, q, e.year); err != nil {
			e.t.Fatalf("purge %q: %v", q, err)
		}
	}
	if _, err := e.pool.ExecContext(e.ctx,
		`DELETE FROM recurring_entries WHERE neighbor_id IN (SELECT id FROM neighbors WHERE name LIKE '%'||$1)`, uname); err != nil {
		e.t.Fatalf("purge recurring: %v", err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM neighbors WHERE name LIKE '%'||$1`, uname); err != nil {
		e.t.Fatalf("purge neighbors: %v", err)
	}
	// audit_log.user_id is ON DELETE SET NULL, i.e. an UPDATE — which 0036's
	// append-only trigger forbids unconditionally. The only sanctioned removal is
	// a DELETE under the treckrr.allow_audit_prune flag (the same opt-in the
	// retention purge uses), so this test's audit rows go first, in one
	// transaction, and the user afterwards.
	tx, err := e.pool.BeginTx(e.ctx, nil)
	if err != nil {
		e.t.Fatalf("purge tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(e.ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
		e.t.Fatalf("purge audit opt-in: %v", err)
	}
	if _, err := tx.ExecContext(e.ctx,
		`DELETE FROM audit_log WHERE user_id IN (SELECT id FROM users WHERE username=$1)`, uname); err != nil {
		e.t.Fatalf("purge audit: %v", err)
	}
	if _, err := tx.ExecContext(e.ctx, `DELETE FROM users WHERE username=$1`, uname); err != nil {
		e.t.Fatalf("purge user: %v", err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatalf("purge commit: %v", err)
	}
}

func (e *itEnv) get(path string) string {
	e.t.Helper()
	resp, err := e.client.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("GET %s -> %d", path, resp.StatusCode)
	}
	return string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func (e *itEnv) csrf(fromPath string) string {
	e.t.Helper()
	m := csrfRe.FindStringSubmatch(e.get(fromPath))
	if m == nil {
		e.t.Fatalf("no csrf token on %s", fromPath)
	}
	return m[1]
}

// post submits a form with the CSRF token and returns the final (redirected)
// body so callers can assert on flash-rendered pages when they want to.
func (e *itEnv) post(path string, form url.Values) string {
	e.t.Helper()
	form.Set("csrf_token", e.csrf("/"))
	resp, err := e.client.PostForm(e.srv.URL+path, form)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		e.t.Fatalf("POST %s -> %d", path, resp.StatusCode)
	}
	return string(b)
}

// ---------------------------------------------------------------------------

var formRe = regexp.MustCompile(`(?is)<form\b[^>]*method="post"[^>]*>.*?</form>`)

// TestCSRFInvariantEveryPostFormCarriesTokenIntegration walks every GET page and
// asserts each rendered POST form contains the injected token — the invariant
// injectCSRFField exists to uphold. A page missing from this list fails loudly
// (404) instead of silently shrinking the walk (Ausbaukarte Nr. 6).
func TestCSRFInvariantEveryPostFormCarriesTokenIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// One entry so the edit page and the beleg have content to render forms for.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-03-02"},
		"hours": {"2"}, "unit": {"h"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) == 0 {
		t.Fatalf("fixture entry missing: %v (n=%d)", err, len(entries))
	}
	eid := entries[0].ID

	pages := []string{
		"/",
		"/neighbors",
		fmt.Sprintf("/neighbors/%d?year=%d", nid, yid),
		fmt.Sprintf("/neighbors/%d/beleg?year=%d", nid, yid),
		fmt.Sprintf("/entries/%d/edit", eid),
		fmt.Sprintf("/prices?base=%d", e.baseID64),
		"/prices/compare",
		fmt.Sprintf("/gespanne?base=%d", e.baseID64),
		"/years",
		fmt.Sprintf("/stats?year=%d", yid),
		fmt.Sprintf("/mahnwesen?year=%d", yid),
		"/recurring",
		"/entries/import",
		"/profile",
		"/account/2fa",
		"/admin/users",
		"/admin/audit",
		"/admin/backup",
		"/admin/company",
	}
	totalForms := 0
	for _, p := range pages {
		body := e.get(p)
		for _, f := range formRe.FindAllString(body, -1) {
			totalForms++
			if !strings.Contains(f, `name="csrf_token"`) {
				snip := f
				if len(snip) > 160 {
					snip = snip[:160]
				}
				t.Errorf("%s: POST form without csrf_token: %s…", p, snip)
			}
		}
	}
	// The walk itself must stay meaningful: if a refactor renamed pages or forms
	// stopped rendering, a "0 forms checked" pass would be worthless.
	if totalForms < 25 {
		t.Errorf("only %d POST forms found across %d pages — the walk has gone stale", totalForms, len(pages))
	}
}

// TestPriceMasterAndBookingHandlersIntegration drives the price master through
// its real handlers and books through the real form path, asserting the money
// that lands in the store (Ausbaukarte Nr. 92).
func TestPriceMasterAndBookingHandlersIntegration(t *testing.T) {
	e := newItEnv(t)

	// Create a second tractor through the handler; German decimal on purpose.
	e.post("/prices/tractors", url.Values{
		"base_id": {itoa64(e.baseID64)}, "ident": {"IT-T2"}, "name": {"Zweiter"},
		"ps": {"80,5"}, "sort_order": {"2"},
	})
	tractors, err := e.st.ListTractors(e.ctx, e.baseID64)
	if err != nil {
		t.Fatalf("list tractors: %v", err)
	}
	var t2 bool
	for _, tr := range tractors {
		if tr.Ident == "IT-T2" && tr.PS.StringFixed(1) == "80.5" {
			t2 = true
		}
	}
	if !t2 {
		t.Fatalf("handler-created tractor missing or PS wrong: %+v", tractors)
	}

	// Rig page must price the fixture rig: 100 PS × 0,36 + 2 m × 5 = 46,00.
	if page := e.get(fmt.Sprintf("/gespanne?base=%d", e.baseID64)); !strings.Contains(page, "46,00") {
		t.Errorf("gespanne page does not show the 46,00 €/h rate")
	}

	// Booking through the form: 2 h × 46,00 = 92,00.
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-04-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	if entries[0].Cost.StringFixed(2) != "92.00" || entries[0].HourlyRate.StringFixed(2) != "46.00" {
		t.Errorf("booked %s at %s/h, want 92.00 at 46.00", entries[0].Cost, entries[0].HourlyRate)
	}
}

// TestRecalcApplyHandlerIntegration proves the full loop: edit the basis through
// the handler, then apply the recalculation through the handler, and the stored
// booking follows (Ausbaukarte Nr. 93).
func TestRecalcApplyHandlerIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-04-02"},
		"hours": {"2"}, "unit": {"h"},
	})

	// Raise the load level 0,36 -> 0,40 through its save handler (an update: id set).
	e.post("/prices/loadlevels", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.loadID)},
		"name": {"it-mittel"}, "cost_per_ps": {"0,40"}, "sort_order": {"1"},
	})

	// Apply for the neighbor: new rate 100×0,40 + 10 = 50, cost 2 h -> 100,00.
	e.post(fmt.Sprintf("/neighbors/%d/recalc", e.neighborID), url.Values{
		"year": {itoa64(e.yearID64)}, "year_id": {itoa64(e.yearID64)},
	})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	if entries[0].HourlyRate.StringFixed(2) != "50.00" || entries[0].Cost.StringFixed(2) != "100.00" {
		t.Errorf("after recalc apply: %s at %s/h, want 100.00 at 50.00",
			entries[0].Cost, entries[0].HourlyRate)
	}
}

// TestInvoiceLifecycleHandlersIntegration: issue -> gutschrift -> storno ->
// batch re-issue, plus payment add/delete/restore — the handlers that mint and
// unmake tax documents, previously untested (Ausbaukarte Nr. 93/94).
func TestInvoiceLifecycleHandlersIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-04-03"},
		"hours": {"2"}, "unit": {"h"},
	})

	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("no invoice after issue: %v", err)
	}
	wantPrefix := fmt.Sprintf("%d-", e.year)
	if !strings.HasPrefix(iv.Number, wantPrefix) {
		t.Errorf("invoice number %q lacks year prefix %q", iv.Number, wantPrefix)
	}

	e.post(fmt.Sprintf("/neighbors/%d/invoice/gutschrift", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"10"}, "note": {"IT-Gutschrift"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/invoice/storno", nid), url.Values{
		"year_id": {itoa64(yid)}, "reason": {"IT-Storno"},
	})
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err == nil {
		t.Error("active invoice still present after storno")
	}

	// The batch run re-issues for the now invoice-less neighbor.
	e.post(fmt.Sprintf("/years/%d/issue-all", yid), url.Values{})
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err != nil {
		t.Errorf("batch issue created no invoice: %v", err)
	}

	// Payments: add, soft-delete, restore.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"20,50"}, "paid_on": {"2026-04-04"}, "note": {"IT"},
	})
	pays, err := e.st.ListPayments(e.ctx, yid, nid)
	if err != nil || len(pays) != 1 {
		t.Fatalf("payments: %v (n=%d)", err, len(pays))
	}
	if pays[0].Amount.StringFixed(2) != "20.50" {
		t.Errorf("payment amount %s, want 20.50", pays[0].Amount)
	}
	e.post(fmt.Sprintf("/payments/%d/delete", pays[0].ID), url.Values{})
	if after, _ := e.st.ListPayments(e.ctx, yid, nid); len(after) != 0 {
		t.Errorf("payment still listed after soft delete")
	}
	e.post(fmt.Sprintf("/payments/%d/restore", pays[0].ID), url.Values{})
	if after, _ := e.st.ListPayments(e.ctx, yid, nid); len(after) != 1 {
		t.Errorf("payment not restored")
	}
}

// TestWebauthnBeginHandlersIntegration covers the registration-begin endpoint
// (password step-up, JSON options) and the new decode guard (Ausbaukarte Nr. 95).
func TestWebauthnBeginHandlersIntegration(t *testing.T) {
	e := newItEnv(t)

	body, _ := json.Marshal(map[string]string{"password": e.adminPass})
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/account/passkeys/register/begin", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf("/profile"))
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "publicKey") {
		t.Errorf("register begin -> %d, body without publicKey options: %.120s", resp.StatusCode, string(b))
	}

	// Undecodable body: 400 from the new guard, not a 403 for an "empty" password.
	req2, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/account/passkeys/register/begin", strings.NewReader("{nicht json"))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-CSRF-Token", e.csrf("/profile"))
	resp2, err := e.client.Do(req2)
	if err != nil {
		t.Fatalf("begin bad json: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("undecodable body -> %d, want 400", resp2.StatusCode)
	}
}

// adminCountLockKey guards every test that either creates an admin user or
// asserts on the global admin count. Session-level, so it is held for the whole
// test and released on Cleanup.
const adminCountLockKey = 918273645

func lockAdminCount(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	// A pinned connection, not the pool: pg_advisory_lock is SESSION-scoped, and
	// database/sql would happily run the unlock on a different connection — the
	// lock would then never be released and the next taker would block forever.
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("admin-count lock conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, adminCountLockKey); err != nil {
		_ = conn.Close()
		t.Fatalf("admin-count advisory lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, adminCountLockKey)
		_ = conn.Close()
	})
}
