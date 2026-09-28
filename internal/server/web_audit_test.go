package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/store"
)

// TestParseImportCSVNumericBounds pins WEB-05: every row that would overflow or
// silently round an entries NUMERIC column is rejected in the preview, so the
// commit never fails halfway through the file.
func TestParseImportCSVNumericBounds(t *testing.T) {
	row := func(unit, qty, price string) string {
		return "Max Mustermann;2026-03-01;X;;;;" + unit + ";" + qty + ";" + price + ";;\n"
	}
	cases := map[string]struct{ line, wantErr string }{
		"hours overflow":      {row("h", "10000000", "1"), "Stunden zu groß"},
		"hours scale":         {row("h", "1,0005", "1"), "Stunden: höchstens 3 Nachkommastellen"},
		"hourly rate":         {row("h", "1", "100000000"), "Stundensatz zu groß"},
		"quantity scale":      {row("Ballen", "1,00001", "1"), "Menge: höchstens 4 Nachkommastellen"},
		"price scale":         {row("Ballen", "1", "0,00001"), "Satz: höchstens 4 Nachkommastellen"},
		"quantity overflow":   {row("Ballen", "1000000000", "1"), "Menge zu groß"},
		"price overflow":      {row("Ballen", "1", "1000000000"), "Satz zu groß"},
		"cost overflow":       {row("Ballen", "100000", "100000"), "Kosten (Menge × Satz) zu groß"},
		"exponent quantity":   {row("Ballen", "1e9", "1"), "Menge muss > 0 sein"},
		"exponent price":      {row("Ballen", "1", "1E3"), "Satz muss > 0 sein"},
		"largest valid hours": {row("h", "9999999,999", "1"), ""},
		"largest valid qty":   {row("Ballen", "99999,9999", "99999,9999"), ""},
		"trailing zero scale": {row("h", "2,50000", "20,000000"), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rows, err := parseImportCSV(tc.line, testMembers())
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Err != tc.wantErr {
				t.Fatalf("err = %q, want %q", errOf(rows), tc.wantErr)
			}
		})
	}
}

// TestImportCommitSummary: the audit detail and the flash both carry the
// failed rows, and the flash names at most five of them.
func TestImportCommitSummary(t *testing.T) {
	failed := []string{"Zeile 2 (a)", "Zeile 3 (b)", "Zeile 4 (c)", "Zeile 5 (d)", "Zeile 6 (e)", "Zeile 7 (f)"}
	audit, flash := importCommitSummary(3, 1, failed)
	if audit != "3 Buchungen importiert, 1 abgewählt, 6 nicht gebucht" {
		t.Errorf("audit = %q", audit)
	}
	if !strings.Contains(flash, "6 Zeile(n) nicht gebucht: Zeile 2 (a)") || !strings.Contains(flash, "…") || strings.Contains(flash, "Zeile 7") {
		t.Errorf("flash = %q", flash)
	}
	if audit, flash := importCommitSummary(2, 0, nil); audit != "2 Buchungen importiert" || flash != "2 Buchung(en) importiert." {
		t.Errorf("clean summary = %q / %q", audit, flash)
	}
}

// TestImportCommitSkipReason separates per-row refusals from infrastructure
// errors (WEB-05): only the latter abort the commit.
func TestImportCommitSkipReason(t *testing.T) {
	for _, err := range []error{store.ErrInvoiceLocked, store.ErrNeighborAnonymized, store.ErrYearCompleted, store.ErrNotFound, store.ErrIdempotencyConflict} {
		if _, ok := importCommitSkipReason(fmt.Errorf("wrapped: %w", err)); !ok {
			t.Errorf("%v not treated as a per-row refusal", err)
		}
	}
	if _, ok := importCommitSkipReason(errors.New("connection reset")); ok {
		t.Error("an infrastructure error was treated as a per-row refusal")
	}
}

// TestPaymentImportSkipReasonAndSummary pins WEB-04: a refused credit is
// reported by name, the rest of the statement is still booked.
func TestPaymentImportSkipReasonAndSummary(t *testing.T) {
	for _, err := range []error{store.ErrNeighborAnonymized, store.ErrNotFound, store.ErrYearCompleted, store.ErrInvoiceLocked} {
		if _, ok := paymentImportSkipReason(fmt.Errorf("wrapped: %w", err)); !ok {
			t.Errorf("%v not treated as a per-credit refusal", err)
		}
	}
	if _, ok := paymentImportSkipReason(errors.New("connection reset")); ok {
		t.Error("an infrastructure error was treated as a per-credit refusal")
	}
	msg := paymentImportSummary(2, []paymentImportSkip{{Label: "2026-001", Reason: "Nachbar anonymisiert"}})
	if msg != "2 Zahlung(en) importiert und zugeordnet. 1 übersprungen: 2026-001 (Nachbar anonymisiert)." {
		t.Errorf("summary = %q", msg)
	}
	if msg := paymentImportSummary(1, nil); msg != "1 Zahlung(en) importiert und zugeordnet." {
		t.Errorf("clean summary = %q", msg)
	}
}

// TestDunningStageParam pins WEB-06: only 0, 1 and 2 (or no stage at all,
// meaning the reminder) are accepted.
func TestDunningStageParam(t *testing.T) {
	for raw, want := range map[string]struct {
		stage int
		ok    bool
	}{
		"": {0, true}, "0": {0, true}, "1": {1, true}, " 2 ": {2, true},
		"3": {0, false}, "-1": {0, false}, "x": {0, false}, "1.0": {0, false}, "99999999999999999999": {0, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/neighbors/1/mahnung?stufe="+url.QueryEscape(raw), nil)
		stage, ok := dunningStageParam(r)
		if stage != want.stage || ok != want.ok {
			t.Errorf("stufe=%q -> (%d, %v), want (%d, %v)", raw, stage, ok, want.stage, want.ok)
		}
	}
}

// TestLimitBodyImportUploadRequiresSession: the import previews' larger body
// allowance is only for signed-in users, like the photo upload's.
func TestLimitBodyImportUploadRequiresSession(t *testing.T) {
	s := testServer()
	h := s.limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an anonymous import upload reached the handler")
	}))
	for _, path := range []string{"/payments/import/preview", "/entries/import/preview"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("x"))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s anonymous status = %d, want 403", path, rr.Code)
		}
	}
	if maxImportUpload <= maxImportPayloadLen || maxImportUpload >= maxPhotoUpload {
		t.Errorf("maxImportUpload = %d, want just above the %d payload cap", maxImportUpload, maxImportPayloadLen)
	}
	if store.MaxImportUploadBytes != maxImportPayloadLen {
		t.Errorf("store cap %d != handler cap %d", store.MaxImportUploadBytes, maxImportPayloadLen)
	}
}
