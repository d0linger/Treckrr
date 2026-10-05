package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// status sends a request through the full chain and returns the final status
// and body without failing on 4xx, for tests that assert on refusals.
func (e *itEnv) status(method, path string, form url.Values) (int, string) {
	e.t.Helper()
	var resp *http.Response
	var err error
	if method == http.MethodGet {
		resp, err = e.client.Get(e.srv.URL + path)
	} else {
		if form == nil {
			form = url.Values{}
		}
		form.Set("csrf_token", e.csrf("/"))
		resp, err = e.client.PostForm(e.srv.URL+path, form)
	}
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// secondNeighbor adds another member of the env's year (purged with the env).
func (e *itEnv) secondNeighbor(prefix string) int64 {
	e.t.Helper()
	id, err := e.st.CreateNeighbor(e.ctx, prefix+" "+e.uname, "")
	if err != nil {
		e.t.Fatalf("neighbor: %v", err)
	}
	if err := e.st.UpdateNeighbor(e.ctx, id, prefix+" "+e.uname, "", "Feldweg 9, 4710 Testdorf", "", "", "", nil); err != nil {
		e.t.Fatalf("address: %v", err)
	}
	if err := e.st.AddNeighborToYear(e.ctx, e.yearID64, id); err != nil {
		e.t.Fatalf("membership: %v", err)
	}
	return id
}

func (e *itEnv) book(nid int64, date string) {
	e.t.Helper()
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {date},
		"hours": {"1"}, "unit": {"h"},
	})
}

// TestPaymentImportByTokenIntegration pins WEB-03 and WEB-04 end to end: a
// statement larger than the 1 MiB body cap previews AND commits (the commit
// sends a token, not the file); a credit for an anonymized neighbor's invoice
// stays unmatched instead of aborting the import; a commit without a live
// token asks for a new upload; an oversized file gets a size message, not a
// CSRF error.
func TestPaymentImportByTokenIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	erased := e.secondNeighbor("Gelöscht")
	e.book(nid, "2026-06-01")
	e.book(erased, "2026-06-02")
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	e.postIssue(erased, url.Values{"year_id": {itoa64(yid)}})
	live, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := e.st.GetInvoice(e.ctx, yid, erased)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AnonymizeNeighbor(e.ctx, erased); err != nil {
		t.Fatal(err)
	}

	nonce := fmt.Sprint(time.Now().UnixNano())
	var csv strings.Builder
	csv.WriteString("Datum;Betrag;Verwendungszweck\n")
	fmt.Fprintf(&csv, "01.07.2026;10,00;Zahlung %s %s\n", live.PaymentReference, nonce)
	fmt.Fprintf(&csv, "02.07.2026;11,00;Zahlung %s %s\n", gone.PaymentReference, nonce)
	// Debit padding: ignored by the parser, but it takes the file past 1 MiB,
	// where the old urlencoded echo of the file failed its commit.
	pad := strings.Repeat("x", 900)
	for csv.Len() < 1_300_000 {
		fmt.Fprintf(&csv, "03.07.2026;-1,00;%s\n", pad)
	}
	page := e.postMultipart("/payments/import/preview", "statement.csv", []byte(csv.String()))
	if strings.Contains(page, `name="raw"`) {
		t.Fatal("the preview still echoes the raw statement into the page")
	}
	token := extractValue(t, page, "upload_token")
	if !strings.Contains(page, "1 von 2 Eingängen") {
		t.Errorf("the anonymized neighbor's credit was matched (want 1 of 2 matched)")
	}

	done := e.post("/payments/import", url.Values{"upload_token": {token}})
	if !strings.Contains(done, "1 Zahlung(en) importiert") {
		t.Errorf("commit flash does not report the booked credit")
	}
	pays, err := e.st.ListPayments(e.ctx, yid, nid)
	if err != nil || len(pays) != 1 || !pays[0].Amount.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("payments = %+v (%v), want the one 10,00 credit", pays, err)
	}
	if erasedPays, _ := e.st.ListPayments(e.ctx, yid, erased); len(erasedPays) != 0 {
		t.Fatalf("a credit was booked on the anonymized account")
	}
	// Re-submitting the same token is a no-op (hash de-dup).
	e.post("/payments/import", url.Values{"upload_token": {token}})
	if again, _ := e.st.ListPayments(e.ctx, yid, nid); len(again) != 1 {
		t.Fatalf("re-commit booked again: %d payments", len(again))
	}

	// No or unknown token: a clear request to upload again, nothing booked.
	for _, tok := range []string{"", "not-a-token"} {
		body := e.post("/payments/import", url.Values{"upload_token": {tok}})
		if !strings.Contains(body, "Vorschau ist abgelaufen") {
			t.Errorf("token %q: no expiry message", tok)
		}
	}

	// A file over the limit is answered with a size message (413), before the
	// CSRF check could misreport the truncated form as a token problem.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", e.csrf("/"))
	fw, _ := mw.CreateFormFile("file", "huge.csv")
	_, _ = fw.Write(bytes.Repeat([]byte("a"), maxImportPayloadLen+100<<10))
	_ = mw.Close()
	resp, err := e.client.Post(e.srv.URL+"/payments/import/preview", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "zu groß") || strings.Contains(string(body), "CSRF") {
		t.Fatalf("oversized upload -> %d %q, want 413 with a size message", resp.StatusCode, body)
	}
}

// TestBookingImportCommitIntegration pins WEB-03 and WEB-05: the commit reads
// the CSV parked under the token (the browser no longer sends it), an
// oversized value is rejected in the preview, and a row whose invoice was
// festgeschrieben after the preview is reported while the others are booked
// and audited.
func TestBookingImportCommitIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	locked := e.secondNeighbor("Gesperrt")
	e.book(locked, "2026-02-01")
	header := "Nachbar;Datum;Tätigkeit;Traktor;Belastung;Maschinen;Einheit;Menge;Satz/Einheit (€);Kosten (€);Notiz\n"
	name, lockedName := "IT-Nachbar "+e.uname, "Gesperrt "+e.uname
	csv := header +
		name + ";2026-10-01;Mähen;;;;h;2;20,00;;\n" +
		lockedName + ";2026-10-02;Mähen;;;;h;1;20,00;;\n" +
		name + ";2026-10-03;Mähen;;;;h;10000000;1;;\n" +
		name + ";2026-10-04;Pressen;;;;Ballen;10;3,00;;\n"
	page := e.post("/entries/import/preview", url.Values{"year_id": {itoa64(yid)}, "csv": {csv}})
	if !strings.Contains(page, "Stunden zu groß") || !strings.Contains(page, "3 von 4 Zeilen importierbar") {
		t.Fatalf("preview did not reject the oversized hours row")
	}
	if strings.Contains(page, `type="hidden" name="csv"`) {
		t.Fatal("the confirm form still posts the CSV back")
	}
	token := extractValue(t, page, "import_token")

	// The second neighbor's invoice is frozen between preview and commit.
	e.postIssue(locked, url.Values{"year_id": {itoa64(yid)}})
	done := e.post("/entries/import", url.Values{
		"year_id": {itoa64(yid)}, "import_token": {token}, "line": {"2", "3", "5"},
	})
	if !strings.Contains(done, "2 Buchung(en) importiert") || !strings.Contains(done, "Zeile 3 (Rechnung festgeschrieben)") {
		t.Errorf("commit flash does not report 2 booked and the locked row")
	}
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 2 {
		t.Fatalf("imported %d rows (%v), want 2", len(entries), err)
	}
	var detail string
	if err := e.pool.QueryRowContext(e.ctx,
		`SELECT detail FROM audit_log WHERE username=$1 AND action='import' ORDER BY id DESC LIMIT 1`, e.uname).Scan(&detail); err != nil {
		t.Fatalf("no import audit row: %v", err)
	}
	if detail != "2 Buchungen importiert, 1 nicht gebucht" {
		t.Errorf("audit detail = %q", detail)
	}

	// A token for another year, or none, never imports.
	for _, tok := range []string{"", token + "x"} {
		body := e.post("/entries/import", url.Values{"year_id": {itoa64(yid)}, "import_token": {tok}, "line": {"2"}})
		if !strings.Contains(body, "Vorschau ist abgelaufen") {
			t.Errorf("token %q: no expiry message", tok)
		}
	}
	if again, _ := e.st.ListEntries(e.ctx, nid, yid); len(again) != 2 {
		t.Fatalf("an invalid token imported rows: %d", len(again))
	}
}

// TestDunningStageAndOpenAmountIntegration pins WEB-06: every dunning handler
// refuses a stage outside 0-2 with 400, and a settled account is neither
// e-mailed nor recorded as dunned.
func TestDunningStageAndOpenAmountIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.book(nid, "2026-04-05")
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	q := fmt.Sprintf("year=%d&stufe=3", yid)
	for _, tc := range []struct {
		method, path string
		form         url.Values
	}{
		{http.MethodGet, fmt.Sprintf("/neighbors/%d/mahnung?%s", nid, q), nil},
		{http.MethodGet, fmt.Sprintf("/neighbors/%d/mahnung.pdf?%s", nid, q), nil},
		{http.MethodGet, fmt.Sprintf("/neighbors/%d/mahnung/epc-qr.png?%s", nid, q), nil},
		{http.MethodPost, fmt.Sprintf("/neighbors/%d/mahnung/email?%s", nid, q), url.Values{}},
		{http.MethodPost, fmt.Sprintf("/neighbors/%d/mahnung/mark-sent", nid), url.Values{"year": {itoa64(yid)}, "stufe": {"3"}}},
		{http.MethodPost, "/mahnwesen/batch-email", url.Values{"year": {itoa64(yid)}, "stufe": {"-1"}}},
	} {
		if code, body := e.status(tc.method, tc.path, tc.form); code != http.StatusBadRequest || !strings.Contains(body, "Mahnstufe") {
			t.Errorf("%s %s -> %d, want 400 with a stage message", tc.method, tc.path, code)
		}
	}
	if code, _ := e.status(http.MethodGet, fmt.Sprintf("/neighbors/%d/mahnung?year=%d&stufe=2", nid, yid), nil); code != http.StatusOK {
		t.Errorf("a valid stage -> %d", code)
	}

	// Settle the invoice in full: nothing is left to dun.
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil || iv.Content == nil {
		t.Fatalf("invoice: %v", err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {iv.Content.Gross.StringFixed(2)}, "paid_on": {"2026-04-06"},
	})
	page := e.post(fmt.Sprintf("/neighbors/%d/mahnung/mark-sent", nid), url.Values{"year": {itoa64(yid)}, "stufe": {"1"}})
	if !strings.Contains(page, "kein Betrag offen") {
		t.Errorf("mark-sent on a settled account gave no refusal")
	}
	page = e.post(fmt.Sprintf("/neighbors/%d/mahnung/email?year=%d&stufe=1", nid, yid), url.Values{})
	if !strings.Contains(page, "kein Betrag offen") {
		t.Errorf("e-mail on a settled account gave no refusal")
	}
	var notices, mails int
	if err := e.pool.QueryRowContext(e.ctx,
		`SELECT (SELECT count(*) FROM dunning_notices WHERE neighbor_id=$1), (SELECT count(*) FROM mail_outbox WHERE neighbor_id=$1)`, nid).Scan(&notices, &mails); err != nil {
		t.Fatal(err)
	}
	if notices != 0 || mails != 0 {
		t.Fatalf("settled account got %d notice(s) and %d mail intent(s)", notices, mails)
	}
}

// TestInvoiceIssueBoundToPreviewIntegration pins WEB-07 for both the single
// Festschreibung and the batch: a booking added after the preview blocks the
// stale confirmation and shows the preview again.
func TestInvoiceIssueBoundToPreviewIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.book(nid, "2026-03-01")
	confirm := e.get(fmt.Sprintf("/neighbors/%d/invoice/confirm?year=%d", nid, yid))
	m := contentHashRe.FindStringSubmatch(confirm)
	if m == nil || len(m[1]) != 64 {
		t.Fatal("the confirm page carries no content hash")
	}
	stale := m[1]
	e.book(nid, "2026-03-02") // another session, after the preview
	page := e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}, "content_hash": {stale}})
	if !strings.Contains(page, "seit der Vorschau geändert") || !strings.Contains(page, `name="content_hash"`) {
		t.Errorf("a stale confirmation did not re-show the preview with the change notice")
	}
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err == nil {
		t.Fatal("an invoice was issued for content the operator never saw")
	}
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err == nil {
		t.Fatal("a confirmation without a hash issued an invoice")
	}
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil || iv.Content == nil || len(iv.Content.Lines) != 2 {
		t.Fatalf("confirmed issue: %v (%+v)", err, iv.Content)
	}

	// Batch: the second neighbor changes after the preview.
	second := e.secondNeighbor("Sammel")
	e.book(second, "2026-03-03")
	preview := e.get(fmt.Sprintf("/years/%d/issue-all", yid))
	form := url.Values{"neighbor_id": {itoa64(second)}}
	for _, hm := range batchHashRe.FindAllStringSubmatch(preview, -1) {
		form.Set(hm[1], hm[2])
	}
	if form.Get(batchHashField(second)) == "" {
		t.Fatal("the batch preview carries no hash for the neighbor")
	}
	e.book(second, "2026-03-04")
	page = e.post(fmt.Sprintf("/years/%d/issue-all", yid), form)
	if !strings.Contains(page, "seit der Vorschau geändert") {
		t.Errorf("the batch did not report the changed neighbor")
	}
	if _, err := e.st.GetInvoice(e.ctx, yid, second); err == nil {
		t.Fatal("the batch issued an invoice for unconfirmed content")
	}
	e.postIssueAll(yid, url.Values{"neighbor_id": {itoa64(second)}})
	if _, err := e.st.GetInvoice(e.ctx, yid, second); err != nil {
		t.Fatalf("the fresh batch confirmation issued nothing: %v", err)
	}
}

// TestPhotoDedupeAuditAndLedgerLinksIntegration pins WEB-10 and WEB-02: a
// repeated upload stores and audits nothing new, and ledger receipts are
// linked under /ledger/ in the gallery and the Art. 15 export.
func TestPhotoDedupeAuditAndLedgerLinksIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.book(nid, "2026-05-01")
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v", err)
	}
	eid := entries[0].ID
	img := pngBytes(t)
	e.postFiles(fmt.Sprintf("/entries/%d/photos", eid), "photo", map[string][]byte{"a.png": img})
	page := e.postFiles(fmt.Sprintf("/entries/%d/photos", eid), "photo", map[string][]byte{"a-again.png": img})
	if !strings.Contains(page, "bereits an dieser Buchung") {
		t.Errorf("the repeated upload was not reported as a duplicate")
	}
	if photos, _ := e.st.ListEntryPhotos(e.ctx, eid); len(photos) != 1 {
		t.Fatalf("booking has %d photos, want 1", len(photos))
	}
	var audits int
	if err := e.pool.QueryRowContext(e.ctx,
		`SELECT count(*) FROM audit_log WHERE username=$1 AND action='photo_add'`, e.uname).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("photo_add audit rows = %d, want 1 (the duplicate stored nothing)", audits)
	}

	lid, err := e.st.CreateLedgerBooking(e.ctx, store.LedgerBookingInput{
		YearID: yid, NeighborID: nid, Date: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), Incoming: true,
		IdempotencyKey: "web02-" + e.uname, Booking: models.LedgerBooking{
			Version: 1, Kind: "fixed", TaskLabel: "Gegenleistung", Unit: "Pauschale",
			Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(20),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.postFiles(fmt.Sprintf("/ledger/%d/photos", lid), "photo", map[string][]byte{"l.png": pngBytesSeed(t, 3)})
	ledgerPrefix := fmt.Sprintf("/ledger/%d/photos/", lid)
	if gallery := e.get(neighborBookingsURL(nid, yid)); !strings.Contains(gallery, `href="`+ledgerPrefix) {
		t.Errorf("the gallery does not link the ledger receipt under %s", ledgerPrefix)
	}
	var export struct {
		BillingYears []struct {
			Photos []struct {
				URL string `json:"url"`
			} `json:"photos"`
		} `json:"billing_years"`
	}
	if err := json.Unmarshal([]byte(e.get(fmt.Sprintf("/neighbors/%d/dsgvo-export.json", nid))), &export); err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, y := range export.BillingYears {
		for _, p := range y.Photos {
			urls = append(urls, p.URL)
		}
	}
	var sawEntry, sawLedger bool
	for _, u := range urls {
		switch {
		case strings.HasPrefix(u, ledgerPrefix):
			sawLedger = true
		case strings.HasPrefix(u, fmt.Sprintf("/entries/%d/photos/", eid)):
			sawEntry = true
		default:
			t.Errorf("export URL %q names neither the booking nor the ledger posting", u)
		}
		// Every exported URL must serve the receipt it names.
		if code, _ := e.status(http.MethodGet, u, nil); code != http.StatusOK {
			t.Errorf("export URL %q -> %d", u, code)
		}
	}
	if !sawEntry || !sawLedger {
		t.Errorf("export URLs %v, want one entry and one ledger photo", urls)
	}
}

// pngBytesSeed is pngBytes with a different pattern, for tests that need two
// distinct images (identical ones are stored once per booking).
func pngBytesSeed(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(seed * 40), G: uint8(x * 20), B: uint8(y * 20), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestNeighborUpdateAnonymizedIntegration pins WEB-11: editing an erased
// neighbor reports an error and writes no audit row.
func TestNeighborUpdateAnonymizedIntegration(t *testing.T) {
	e := newItEnv(t)
	gone := e.secondNeighbor("Anonym")
	if err := e.st.AnonymizeNeighbor(e.ctx, gone); err != nil {
		t.Fatal(err)
	}
	page := e.post(fmt.Sprintf("/neighbors/%d/update", gone), url.Values{
		"name": {"Wiederbelebt " + e.uname}, "iban": {"AT611904300234573201"},
	})
	if !strings.Contains(page, "können nicht mehr bearbeitet werden") || strings.Contains(page, "Nachbar aktualisiert.") {
		t.Errorf("the refused update was not reported as an error")
	}
	var audits int
	if err := e.pool.QueryRowContext(e.ctx,
		`SELECT count(*) FROM audit_log WHERE action='update' AND entity='neighbor' AND entity_id=$1`, itoa64(gone)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 {
		t.Fatalf("the refused update wrote %d audit row(s)", audits)
	}
	n, err := e.st.GetNeighbor(e.ctx, gone)
	if err != nil || n.IBAN != "" || strings.HasPrefix(n.Name, "Wiederbelebt") {
		t.Fatalf("erased neighbor changed: %+v (%v)", n, err)
	}
}
