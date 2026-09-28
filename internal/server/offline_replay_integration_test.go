package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// replayResult is one offline replay as offline.js sends it.
type replayResult struct {
	status int
	header http.Header
	body   string
}

// replay POSTs form like the offline queue: X-Offline-Replay, a fresh CSRF
// token, no redirect following, and optionally JSON for per-row answers.
func (e *itEnv) replay(path string, form url.Values, acceptJSON bool) replayResult {
	e.t.Helper()
	form.Set("csrf_token", e.csrf("/"))
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Offline-Replay", "1")
	if acceptJSON {
		req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	}
	client := *e.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return replayResult{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func (e *itEnv) countEntries(where string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM entries WHERE `+where, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *itEnv) countAudit(action string, entityIDs ...int64) int {
	e.t.Helper()
	ids := make([]string, len(entityIDs))
	for i, id := range entityIDs {
		ids[i] = itoa64(id)
	}
	var n int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log
		WHERE action=$1 AND entity='entry' AND entity_id = ANY(string_to_array($2, ','))`,
		action, strings.Join(ids, ",")).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *itEnv) setYearStatus(status string) {
	e.t.Helper()
	if err := e.st.SetYearStatus(e.ctx, e.yearID64, status); err != nil {
		e.t.Fatal(err)
	}
}

// TestReplayDedupesBeforeValidationIntegration covers SYN-01/02/08: a booking
// stored before its answer was lost stays "stored" on every later retry, even
// after the catalog changed or the year was closed, while a retry with other
// data under the same key is reported as already stored — never re-created and
// never silently acknowledged. The audit row is written with the booking, once.
func TestReplayDedupesBeforeValidationIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	defer e.setYearStatus(models.YearInProgress)

	t.Run("unified form after a catalog change", func(t *testing.T) {
		t.Cleanup(func() { _ = e.st.SetTractorActive(e.ctx, e.tractorID, true) })
		key := e.uname + "-v2"
		form := func(hours string) url.Values {
			return url.Values{"booking_form_version": {"2"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
				"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {"2026-05-04"}, "mode": {"manual"},
				"tractor_id": {itoa64(e.tractorID)}, "load_level_id": {itoa64(e.loadID)}, "machine_ids": {itoa64(e.machineID)},
				"hours": {hours}, "idempotency_key": {key}}
		}
		if res := e.replay("/entries", form("2"), true); res.status != http.StatusNoContent {
			t.Fatalf("first replay = %d %s", res.status, res.body)
		}
		// The tractor is deactivated after the booking was stored (the answer was lost).
		if err := e.st.SetTractorActive(e.ctx, e.tractorID, false); err != nil {
			t.Fatal(err)
		}
		if res := e.replay("/entries", form("2"), true); res.status != http.StatusNoContent {
			t.Fatalf("identical retry after a catalog change = %d %s, want 204", res.status, res.body)
		}
		res := e.replay("/entries", form("3"), true)
		if res.status != http.StatusUnprocessableEntity || res.header.Get("X-Treckrr-Replay") != "stored" ||
			!strings.Contains(res.body, "Bereits gespeichert (abweichend)") {
			t.Fatalf("changed retry = %d %q %s, want 422 stored-different", res.status, res.header.Get("X-Treckrr-Replay"), res.body)
		}
		if n := e.countEntries(`idempotency_key=$1`, key); n != 1 {
			t.Fatalf("%d entries under the key, want exactly 1", n)
		}
	})

	t.Run("legacy form after the year was closed", func(t *testing.T) {
		key := e.uname + "-legacy"
		form := func(hours string) url.Values {
			return url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
				"entry_date": {"2026-05-05"}, "hours": {hours}, "unit": {"h"}, "idempotency_key": {key}}
		}
		if res := e.replay("/entries", form("2"), false); res.status != http.StatusNoContent {
			t.Fatalf("first replay = %d %s", res.status, res.body)
		}
		var id int64
		if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM entries WHERE idempotency_key=$1`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if n := e.countAudit("create", id); n != 1 {
			t.Fatalf("%d create audits for the booking, want 1 (written with it)", n)
		}
		e.setYearStatus(models.YearCompleted)
		if res := e.replay("/entries", form("2"), false); res.status != http.StatusNoContent {
			t.Fatalf("identical retry in a closed year = %d %s, want 204", res.status, res.body)
		}
		if res := e.replay("/entries", form("5"), false); res.status != http.StatusUnprocessableEntity || res.header.Get("X-Treckrr-Replay") != "stored" {
			t.Fatalf("changed retry = %d %s, want 422 stored-different", res.status, res.body)
		}
		e.setYearStatus(models.YearInProgress)
		if n := e.countAudit("create", id); n != 1 {
			t.Fatalf("%d create audits after retries, want still 1", n)
		}
	})

	t.Run("quick batch after the year was closed", func(t *testing.T) {
		k1, k2 := e.uname+"-q1", e.uname+"-q2"
		batch := func(hours1 string) url.Values {
			return url.Values{"year_id": {yid}, "neighbor_id": {nid},
				"q_date": {"2026-05-06", "2026-05-07"}, "q_gespann": {itoa64(e.gespannID), itoa64(e.gespannID)},
				"q_hours": {hours1, "2"}, "q_key": {k1, k2}}
		}
		if res := e.replay("/entries/quick", batch("1"), true); res.status != http.StatusNoContent {
			t.Fatalf("first replay = %d %s", res.status, res.body)
		}
		var ids []int64
		rows, err := e.pool.QueryContext(e.ctx, `SELECT id FROM entries WHERE idempotency_key IN ($1,$2)`, k1, k2)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		_ = rows.Close()
		if len(ids) != 2 || e.countAudit("quick_create", ids...) != 2 {
			t.Fatalf("rows %v with %d quick_create audits, want 2 rows audited in their transactions", ids, e.countAudit("quick_create", ids...))
		}
		e.setYearStatus(models.YearCompleted)
		if res := e.replay("/entries/quick", batch("1"), true); res.status != http.StatusNoContent {
			t.Fatalf("identical batch retry in a closed year = %d %s, want 204", res.status, res.body)
		}
		e.setYearStatus(models.YearInProgress)
		// SYN-03: a saved row "corrected" under its old key is a conflict, not a
		// silent no-op, and the answer names each row's state by key.
		res := e.replay("/entries/quick", batch("4"), true)
		if res.status != http.StatusUnprocessableEntity {
			t.Fatalf("changed batch retry = %d %s, want 422", res.status, res.body)
		}
		var reply struct {
			Message string
			Rows    map[string]struct{ Status, Message string }
		}
		if err := json.Unmarshal([]byte(res.body), &reply); err != nil {
			t.Fatalf("JSON answer: %v (%s)", err, res.body)
		}
		if reply.Rows[k1].Status != "conflict" || reply.Rows[k2].Status != "saved" {
			t.Fatalf("row states = %+v, want k1 conflict and k2 saved", reply.Rows)
		}
		if n := e.countEntries(`idempotency_key IN ($1,$2)`, k1, k2); n != 2 {
			t.Fatalf("%d keyed rows, want still 2", n)
		}
		if e.countEntries(`idempotency_key=$1 AND hours=1`, k1) != 1 {
			t.Fatal("the stored row changed")
		}
	})
}

// TestReplayValidationIntegration covers SYN-12 and WEB-08: an empty or invalid
// date is a rejected row, never "today", and legacy and quick bookings must use
// the year's price basis and, for a new booking picked by hand, active items.
func TestReplayValidationIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)

	res := e.replay("/entries/quick", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"q_date": {"", "2026-05-08"}, "q_gespann": {itoa64(e.gespannID), itoa64(e.gespannID)},
		"q_hours": {"1", "2"}, "q_key": {e.uname + "-d1", e.uname + "-d2"}}, false)
	if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "Zeile 1: Bitte ein gültiges Datum angeben.") {
		t.Fatalf("quick row without date = %d %s", res.status, res.body)
	}
	if e.countEntries(`idempotency_key=$1`, e.uname+"-d1") != 0 || e.countEntries(`idempotency_key=$1`, e.uname+"-d2") != 1 {
		t.Fatal("the dated row must save and the undated one must not")
	}
	res = e.replay("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
		"hours": {"2"}, "unit": {"h"}, "idempotency_key": {e.uname + "-nodate"}}, false)
	if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "gültiges Datum") {
		t.Fatalf("legacy booking without date = %d %s", res.status, res.body)
	}

	// A hand-picked deactivated tractor is refused for a new legacy booking.
	if err := e.st.SetTractorActive(e.ctx, e.tractorID, false); err != nil {
		t.Fatal(err)
	}
	res = e.replay("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "mode": {"manual"},
		"tractor_id": {itoa64(e.tractorID)}, "load_level_id": {itoa64(e.loadID)}, "entry_date": {"2026-05-09"},
		"hours": {"2"}, "unit": {"h"}, "idempotency_key": {e.uname + "-inactive"}}, false)
	if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "inaktiv") {
		t.Fatalf("inactive tractor = %d %s", res.status, res.body)
	}
	if err := e.st.SetTractorActive(e.ctx, e.tractorID, true); err != nil {
		t.Fatal(err)
	}

	// A gespann of another price basis is refused in quick entry and legacy form.
	// A crashed earlier run may have left the basis behind (year is unique).
	if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM price_bases WHERE year=$1`, e.year+1000); err != nil {
		t.Fatal(err)
	}
	otherBase, err := e.st.CreateEmptyBase(e.ctx, e.year+1000, "IT-Fremdbasis")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(e.ctx, `DELETE FROM price_bases WHERE id=$1`, otherBase) })
	foreignMachine, err := e.st.CreateMachine(e.ctx, otherBase, "IT-Fremdmaschine", decimal.RequireFromString("2"), decimal.RequireFromString("5"), "IT", 1, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.st.CreateGespann(e.ctx, otherBase, "IT-Fremdgespann", nil, nil, []int64{foreignMachine}, 1)
	if err != nil {
		t.Fatal(err)
	}
	res = e.replay("/entries/quick", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"q_date": {"2026-05-10"}, "q_gespann": {itoa64(foreign)}, "q_hours": {"1"}, "q_key": {e.uname + "-foreign"}}, false)
	if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "Preisgrundlage") {
		t.Fatalf("quick row with a foreign gespann = %d %s", res.status, res.body)
	}
	res = e.replay("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(foreign)},
		"entry_date": {"2026-05-10"}, "hours": {"2"}, "unit": {"h"}, "idempotency_key": {e.uname + "-foreign-legacy"}}, false)
	if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "Preisgrundlage") {
		t.Fatalf("legacy booking with a foreign gespann = %d %s", res.status, res.body)
	}
	if e.countEntries(`gespann_id=$1`, foreign) != 0 {
		t.Fatal("a booking was priced from a foreign basis")
	}
}
