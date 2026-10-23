//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestLegacyCompanionUsesMeasuredHoursIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/bases/"+itoa64(e.baseID64)+"/billing-rule", url.Values{"billing_increment_h": {"0,25"}, "minimum_h": {"1"}})
	pid, err := e.st.CreatePerson(e.ctx, "Synthetic helper "+e.uname, decimal.NewFromInt(20), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(context.Background(), `DELETE FROM persons WHERE id=$1`, pid) })
	e.post("/entries", url.Values{"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"entry_date": {"2026-05-01"}, "gespann_id": {itoa64(e.gespannID)}, "hours": {"0.5"}, "unit": {"h"}, "person_id": {itoa64(pid)}})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 2 {
		t.Fatalf("paired booking: %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.PersonID != nil {
			if !entry.Quantity.Equal(decimal.RequireFromString("0.5")) || !entry.Cost.Equal(decimal.NewFromInt(10)) {
				t.Fatalf("helper inherited machine rounding: %+v", entry)
			}
		} else if !entry.Hours.Equal(decimal.NewFromInt(1)) {
			t.Fatalf("machine minimum lost: %+v", entry)
		}
	}
}

func TestLegacyLongNoteCanBeSignedIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"entry_date": {"2026-05-01"}, "gespann_id": {itoa64(e.gespannID)}, "hours": {"1"}, "note": {strings.Repeat("Ä", 500)}})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 {
		t.Fatalf("legacy booking: %+v %v", entries, err)
	}
	entry := entries[0]
	// Historical rows may have no task label; preserve their actual stored data.
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE entries SET task_label='' WHERE id=$1`, entry.ID); err != nil {
		t.Fatal(err)
	}
	entry.TaskLabel = ""
	entry.Signature = &models.EntrySignature{SignerName: "Synthetic signer", SignedAt: time.Now(), Image: []byte("synthetic stored image")}
	if err := e.st.AddEntrySignature(e.ctx, &entry); err != nil {
		t.Fatal(err)
	}
	sig, err := e.st.GetEntrySignature(e.ctx, entry.ID)
	if err != nil || utf8.RuneCountInString(sig.ConfirmedText) != 500 {
		t.Fatalf("signature: %+v %v", sig, err)
	}
	stored, err := e.st.GetEntry(e.ctx, entry.ID)
	if err != nil || stored.Note != entry.Note || !stored.Cost.Equal(entry.Cost) {
		t.Fatal("signing changed booking values")
	}
}

func TestSharedPhotoUploadAcknowledgesOnlyCompleteUploadsIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"entry_date": {"2026-05-01"}, "gespann_id": {itoa64(e.gespannID)}, "hours": {"1"}})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 {
		t.Fatalf("photo booking: %v %v", entries, err)
	}
	id := entries[0].ID
	upload := func(files ...[]byte) (int, bool) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("csrf_token", e.csrf("/")); err != nil {
			t.Fatal(err)
		}
		for _, data := range files {
			part, err := writer.CreateFormFile("photo", "synthetic.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(e.ctx, http.MethodPost, e.srv.URL+"/entries/"+itoa64(id)+"/photos", &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("X-Treckrr-Share-Upload", "1")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result struct {
			Uploaded bool `json:"uploaded"`
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("upload acknowledgement: %s: %v", data, err)
		}
		return resp.StatusCode, result.Uploaded
	}
	photo := pngBytes(t)
	if status, ok := upload(photo, []byte("invalid image")); status != http.StatusUnprocessableEntity || ok {
		t.Fatalf("partial upload acknowledged: %d %v", status, ok)
	}
	if status, ok := upload(photo); status != http.StatusOK || !ok {
		t.Fatalf("retry of saved photo: %d %v", status, ok)
	}
	counts, err := e.st.PhotoCounts(e.ctx, e.yearID64, e.neighborID)
	if err != nil || counts[id] != 1 {
		t.Fatalf("retry duplicated photo: %+v %v", counts, err)
	}
}

func TestMeterContinuityAcrossRenamedBasisTractorsIntegration(t *testing.T) {
	e := newItEnv(t)
	book := func(date, start, end string) {
		e.post("/entries", url.Values{"booking_form_version": {"3"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
			"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)}, "entry_date": {date}, "mode": {"gespann"},
			"gespann_id": {itoa64(e.gespannID)}, "hours": {"1"}, "meter_start": {start}, "meter_end": {end}})
	}
	book("2026-07-01", "100", "101")
	book("2026-07-02", "100.5", "101.5")
	base, err := e.st.CreateEmptyBase(e.ctx, e.year+100000, "Other basis "+e.uname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(context.Background(), `DELETE FROM price_bases WHERE id=$1`, base) })
	var original int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT tractor_id FROM gespanne WHERE id=$1`, e.gespannID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	tractor, err := e.st.GetTractor(e.ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.st.CreateTractor(e.ctx, base, tractor.Ident, "Renamed tractor", tractor.PS, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE entries SET tractor_id=$1 WHERE neighbor_id=$2 AND entry_date='2026-07-02'`, other, e.neighborID); err != nil {
		t.Fatal(err)
	}
	report, err := e.st.YearDataQuality(e.ctx, e.yearID64, e.baseID64)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range report.Issues {
		if issue.Code == "meter_overlap" {
			return
		}
	}
	t.Fatal("renaming the same tractor in another basis hid the meter overlap")
}
