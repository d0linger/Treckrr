package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestUnifiedEntryDate requires explicit dates in unified forms while retaining
// the current-time fallback used by legacy clients and queued submissions.
func TestUnifiedEntryDate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, date  string
		invalid, fallback bool
	}{
		{name: "explicit missing", kind: "quantity", invalid: true},
		{name: "explicit malformed", kind: "quantity", date: "not-a-date", invalid: true},
		{name: "explicit impossible", kind: "quantity", date: "2026-02-30", invalid: true},
		{name: "explicit non-leap day", kind: "quantity", date: "2026-02-29", invalid: true},
		{name: "explicit leap day", kind: "quantity", date: "2024-02-29"},
		{name: "explicit trimmed", kind: " quantity ", date: " 2026-09-13 "},
		{name: "legacy missing", fallback: true},
		{name: "legacy malformed", date: "not-a-date", fallback: true},
		{name: "legacy impossible", date: "2026-02-30", fallback: true},
		{name: "legacy valid", date: "2026-09-13"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"unit": {"ha"}, "quantity": {"2"}, "unit_price": {"30"},
				"task_label": {"Ernte"}, "entry_date": {tc.date}}
			if tc.kind != "" {
				form.Set("booking_kind", tc.kind)
			}
			r := httptest.NewRequest("POST", "/entries", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			before := time.Now()
			entry, ids, msg, err := (&Server{}).resolveUnifiedEntryFromForm(r)
			after := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				if entry != nil || len(ids) != 0 || msg != "Bitte ein gültiges Datum angeben." {
					t.Fatalf("invalid date accepted: entry=%+v ids=%v message=%q", entry, ids, msg)
				}
				return
			}
			if entry == nil || msg != "" {
				t.Fatalf("valid/legacy form rejected: entry=%+v message=%q", entry, msg)
			}
			if tc.fallback {
				if entry.Date.Before(before) || entry.Date.After(after) {
					t.Fatalf("legacy fallback date = %v, outside %v .. %v", entry.Date, before, after)
				}
			} else if got := entry.Date.Format("2006-01-02"); got != strings.TrimSpace(tc.date) {
				t.Errorf("date = %s, want %s", got, tc.date)
			}
		})
	}
}

// TestUnifiedBookingSelection ensures old queues retain their meaning and new
// direction/type values cannot fall silently into an unrelated pricing branch.
func TestUnifiedBookingSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, kind, direction string
		valid                       bool
	}{
		{"legacy hours", "unit=h", "equipment", "out", true},
		{"legacy quantity", "unit=ha", "quantity", "out", true},
		{"incoming labor", "booking_kind=labor&booking_direction=in", "labor", "in", true},
		{"invalid kind", "booking_kind=mistake", "", "", false},
		{"invalid direction", "booking_direction=credit", "", "", false},
		{"hour as quantity", "booking_kind=quantity&unit=h", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/entries", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			kind, direction, msg := unifiedBookingSelection(r)
			if (msg == "") != tc.valid || kind != tc.kind || direction != tc.direction {
				t.Fatalf("got %q %q %q", kind, direction, msg)
			}
		})
	}
}

// TestPositiveBookingDecimal covers locale input and numeric resource boundaries.
func TestPositiveBookingDecimal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"whole", "2", true}, {"decimal comma", "2,5", true}, {"precision", "0.0001", true},
		{"empty", "", false}, {"negative", "-1", false}, {"zero", "0", false},
		{"nan", "NaN", false}, {"infinity", "Infinity", false}, {"exponent", "1e9", false},
		{"excessive precision", "0.00001", false}, {"overflow", "1000000000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/entries", strings.NewReader(url.Values{"value": {tc.raw}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_, valid := positiveBookingDecimal(r, "value")
			if valid != tc.valid {
				t.Fatalf("%q validity=%t", tc.raw, valid)
			}
		})
	}
}

// TestLedgerBookingFromForm verifies service breakdown, authoritative totals and
// validation of every counterclaim shape without a database or browser dependency.
func TestLedgerBookingFromForm(t *testing.T) {
	t.Parallel()
	base := url.Values{"year_id": {"1"}, "neighbor_id": {"2"}, "entry_date": {"2026-09-13"},
		"task_label": {"Ernte"}, "hours": {"2"}, "partner_label": {"Nachbars Traktor"},
		"partner_rate": {"50"}, "partner_person": {"Franz"}, "partner_person_rate": {"20"},
		"partner_person_hours": {"3.5"}, "person_hours": {"99"}, "amount": {"999"}}
	for _, tc := range []struct{ name, kind, field, value, want string }{
		{"equipment with independent helper", "equipment", "", "", "170.00"},
		{"equipment same hours", "equipment", "partner_person_hours", "", "140.00"},
		{"incomplete helper", "equipment", "partner_person", "", ""},
		{"labor", "labor", "", "", "40.00"},
		{"fixed", "fixed", "", "", "999.00"},
		{"missing vehicle", "equipment", "partner_label", "", ""},
		{"invalid helper time", "equipment", "partner_person_hours", "-3", ""},
		{"missing helper rate", "equipment", "partner_person_rate", "", ""},
		{"invalid date", "equipment", "entry_date", "2026-99-99", ""},
		{"missing task", "fixed", "task_label", "", ""},
		{"negative amount", "fixed", "amount", "-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{}
			for key, value := range base {
				values[key] = append([]string{}, value...)
			}
			if tc.field != "" {
				values.Set(tc.field, tc.value)
			}
			r := httptest.NewRequest("POST", "/entries", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			in, msg := ledgerBookingFromForm(r, tc.kind, "in")
			if tc.want == "" {
				if msg == "" {
					t.Fatal("accepted invalid form")
				}
				return
			}
			if msg != "" || in.Booking.Total().StringFixed(2) != tc.want {
				t.Fatalf("total=%s error=%s, want %s", in.Booking.Total(), msg, tc.want)
			}
		})
	}
}

// TestUnifiedRequestFingerprint excludes transport/inactive controls but binds
// billing direction, kind and independent helper inputs to a single retry key.
func TestUnifiedRequestFingerprint(t *testing.T) {
	t.Parallel()
	base := url.Values{"booking_kind": {"equipment"}, "booking_direction": {"out"}, "mode": {"manual"},
		"machine_ids": {"2", "1"}, "hours": {"2"}, "person_id": {"1"}, "person_hours": {"3.5"}, "person_rate": {"20"}}
	fingerprint := func(values url.Values) string {
		r := httptest.NewRequest("POST", "/entries", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return unifiedRequestFingerprint(r)
	}
	original := fingerprint(base)
	if original == "" {
		t.Fatal("new unified form lacks fingerprint")
	}
	for _, tc := range []struct {
		name, key, value string
		same             bool
	}{
		{"csrf refresh", "csrf_token", "different", true},
		{"replay key", "idempotency_key", "different", true},
		{"inactive quantity", "quantity", "99", true},
		{"helper hours", "person_hours", "4", false},
		{"helper rate", "person_rate", "25", false},
		{"kind", "booking_kind", "labor", false},
		{"direction", "booking_direction", "in", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{}
			for key, value := range base {
				values[key] = append([]string{}, value...)
			}
			values.Set(tc.key, tc.value)
			if (fingerprint(values) == original) != tc.same {
				t.Fatal("incorrect retry identity")
			}
		})
	}
	base["machine_ids"] = []string{"1", "2"}
	if fingerprint(base) != original {
		t.Fatal("machine selection order changed retry identity")
	}
	base.Del("booking_kind")
	if fingerprint(base) != "" {
		t.Fatal("legacy form semantics changed")
	}
}
