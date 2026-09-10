package server

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The Schnellerfassung with a helper per row — the same instrument the single
// booking form's person select is, one row at a time. Two properties matter and
// neither is visible from the flash alone: the row books a LINKED Mannstunden
// companion over its own hours, and a row naming a helper the master data
// cannot price is skipped WHOLE. Booking only the machine half there would file
// the work as unmanned while reporting success — the operator asked for both.
func TestQuickEntriesWithPersonIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	pname := "Schnell-Helfer " + e.uname
	// The harness purge does not touch persons (they are master data, not
	// per-year fixtures), so this test removes its own. Registered after
	// newItEnv's cleanups, so it runs BEFORE them — the entries referencing the
	// helper are still there, which the ON DELETE SET NULL column allows.
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM persons WHERE name LIKE '%'||$1`, e.uname)
	})

	e.post("/personen", url.Values{"name": {pname}, "hourly_rate": {"20"}})
	persons, err := e.st.ActivePersons(e.ctx)
	if err != nil {
		t.Fatalf("persons: %v", err)
	}
	var pid int64
	for _, p := range persons {
		if p.Name == pname {
			pid = p.ID
		}
	}
	if pid == 0 {
		t.Fatalf("person %q was not created", pname)
	}
	// The column exists only once a helper does.
	if page := e.get(neighborURL(nid, yid)); !strings.Contains(page, `name="q_person"`) {
		t.Fatalf("the quick-entry table offers no person column")
	}

	// Row 1: 2 h on the fixture rig (46,00 €/h) with the helper (20,00 €/h).
	// Row 2: a helper id the Personenstamm does not know — must be skipped whole.
	body := e.post("/entries/quick", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"q_date":    {"2026-08-01", "2026-08-02"},
		"q_gespann": {itoa64(e.gespannID), itoa64(e.gespannID)},
		"q_hours":   {"2", "3"},
		"q_person":  {itoa64(pid), "999999999"},
	})
	if !strings.Contains(body, "übersprungen") {
		t.Errorf("the flash does not report the skipped row")
	}

	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want exactly 2 (the pair of the one valid row)", len(entries))
	}
	var machine, companion int64
	for _, en := range entries {
		switch en.Unit {
		case "h":
			machine = en.ID
			if en.Cost.StringFixed(2) != "92.00" {
				t.Errorf("machine booking = %s €, want 92.00", en.Cost.StringFixed(2))
			}
			if en.LinkedEntryID != nil {
				t.Errorf("the machine half must not carry the link, only the companion does")
			}
		case "Mannstunde":
			companion = en.ID
			if en.Quantity.StringFixed(2) != "2.00" || en.UnitPrice.StringFixed(2) != "20.00" || en.Cost.StringFixed(2) != "40.00" {
				t.Errorf("companion = %s × %s = %s, want 2,00 × 20,00 = 40,00",
					en.Quantity.StringFixed(2), en.UnitPrice.StringFixed(2), en.Cost.StringFixed(2))
			}
			// Hours stay zero on a Mannstunden booking: the year's hour total
			// counts machine hours once, not twice.
			if !en.Hours.IsZero() {
				t.Errorf("companion carries %s hours, want 0", en.Hours.String())
			}
			if en.PersonID == nil || *en.PersonID != pid {
				t.Errorf("companion is not attributed to the helper: %v", en.PersonID)
			}
		default:
			t.Errorf("unexpected unit %q", en.Unit)
		}
	}
	if machine == 0 || companion == 0 {
		t.Fatalf("expected one machine booking and one companion, got %d/%d", machine, companion)
	}
	partner, err := e.st.LinkedPartnerID(e.ctx, machine)
	if err != nil || partner != companion {
		t.Fatalf("machine booking is not linked to its companion: %d (%v)", partner, err)
	}

	// A clean batch says so, and says how many helpers came with it.
	body = e.post("/entries/quick", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"q_date": {"2026-08-03"}, "q_gespann": {itoa64(e.gespannID)},
		"q_hours": {"1"}, "q_person": {itoa64(pid)},
	})
	if !strings.Contains(body, "Mannstunden gespeichert") {
		t.Errorf("the success flash does not mention the booked Mannstunden")
	}
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	if len(entries) != 4 {
		t.Fatalf("got %d entries after the second batch, want 4", len(entries))
	}

	for _, replay := range []bool{false, true} {
		name := "online"
		if replay {
			name = "offline_replay"
		}
		t.Run(name+"_companion_only_partial_success", func(t *testing.T) {
			key := e.uname + "-" + name
			form := url.Values{
				"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
				"q_date": {"2026-08-04"}, "q_gespann": {itoa64(e.gespannID)},
				"q_hours": {"1"}, "q_key": {key},
			}
			e.post("/entries/quick", form)
			form["q_gespann"] = append(form["q_gespann"], itoa64(e.gespannID))
			form["q_hours"] = append(form["q_hours"], "")
			form["q_person"] = []string{itoa64(pid), ""}
			var body string
			if replay {
				form.Set("csrf_token", e.csrf("/"))
				req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/entries/quick", strings.NewReader(form.Encode()))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("X-Offline-Replay", "1")
				resp, err := e.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("partial replay status = %d, want 422", resp.StatusCode)
				}
				body = string(b)
			} else {
				body = e.post("/entries/quick", form)
			}
			body = html.UnescapeString(body)
			if !strings.Contains(body, "0 Buchung(en) + 1 Mannstunden gespeichert") || !strings.Contains(body, "1 Zeile(n)") {
				t.Errorf("partial success did not report both the new companion and skipped row")
			}
			var count int
			if err := e.pool.QueryRowContext(e.ctx,
				`SELECT count(*) FROM entries WHERE idempotency_key IN ($1, $2)`, key, key+"-p").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Errorf("got %d keyed entries, want one machine and one companion", count)
			}
		})
	}
}
