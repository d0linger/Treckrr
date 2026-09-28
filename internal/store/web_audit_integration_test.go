//go:build integration

package store_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestUpdateNeighborReportsRefusal pins WEB-11: an update the anonymized guard
// refuses is an error, not a silent success the handler would audit.
func TestUpdateNeighborReportsRefusal(t *testing.T) {
	st, _, _, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	if err := st.UpdateNeighbor(ctx, neighborID, "Review neighbor", "n", "Addr 1", "", "", "", nil); err != nil {
		t.Fatalf("regular update: %v", err)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatalf("anonymize: %v", err)
	}
	err := st.UpdateNeighbor(ctx, neighborID, "Revived Name", "", "Revived Street", "", "", "AT611904300234573201", nil)
	if !errors.Is(err, store.ErrNeighborAnonymized) {
		t.Fatalf("update of an anonymized neighbor = %v, want ErrNeighborAnonymized", err)
	}
	n, err := st.GetNeighbor(ctx, neighborID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Name == "Revived Name" || n.Address != "" || n.IBAN != "" {
		t.Fatalf("erased data was repopulated: %+v", n)
	}
	if err := st.UpdateNeighbor(ctx, 1<<50, "Nobody", "", "", "", "", "", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of an unknown neighbor = %v, want ErrNotFound", err)
	}
}

// TestPhotoDedupePerBooking pins WEB-10: re-submitting the same (re-encoded)
// image to a booking stores it once; the same image on another booking and a
// different image on the same booking are still stored.
func TestPhotoDedupePerBooking(t *testing.T) {
	st, _, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	newEntry := func(key string) int64 {
		t.Helper()
		id, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: neighborID, BillingYearID: yearID, Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			TaskLabel: "Photo", Unit: "h", Hours: dec("1"), HourlyRate: dec("10"), Cost: dec("10"), IdempotencyKey: key,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	e1, e2 := newEntry("photo-dedupe-1"), newEntry("photo-dedupe-2")
	img := bytes.Repeat([]byte("jpeg-bytes"), 100)
	if _, err := st.AddEntryPhoto(ctx, e1, img, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddEntryPhoto(ctx, e1, img, "image/jpeg"); !errors.Is(err, store.ErrDuplicatePhoto) {
		t.Fatalf("second identical photo = %v, want ErrDuplicatePhoto", err)
	}
	if _, err := st.AddEntryPhoto(ctx, e1, append(bytes.Clone(img), 'x'), "image/jpeg"); err != nil {
		t.Fatalf("different photo on the same booking: %v", err)
	}
	if _, err := st.AddEntryPhoto(ctx, e2, img, "image/jpeg"); err != nil {
		t.Fatalf("same photo on another booking: %v", err)
	}
	if photos, err := st.ListEntryPhotos(ctx, e1); err != nil || len(photos) != 2 {
		t.Fatalf("booking 1 photos = %d (%v), want 2", len(photos), err)
	}

	ledgerID, err := st.CreateLedgerBooking(ctx, store.LedgerBookingInput{
		YearID: yearID, NeighborID: neighborID, Date: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), Incoming: true,
		IdempotencyKey: "photo-dedupe-ledger", Booking: models.LedgerBooking{
			Version: 1, Kind: "fixed", TaskLabel: "Gegenleistung", Unit: "Pauschale", Quantity: dec("1"), UnitPrice: dec("20"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLedgerPhoto(ctx, ledgerID, img, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLedgerPhoto(ctx, ledgerID, img, "image/jpeg"); !errors.Is(err, store.ErrDuplicatePhoto) {
		t.Fatalf("second identical ledger photo = %v, want ErrDuplicatePhoto", err)
	}
}

// TestNeighborPhotoURLsBySource pins WEB-02: the gallery/export URL follows
// the photo's source table, so ids that coincide across entry_photos and
// ledger_photos never address the other table.
func TestNeighborPhotoURLsBySource(t *testing.T) {
	for _, tc := range []struct {
		ref  store.PhotoRef
		want string
	}{
		{store.PhotoRef{PhotoID: 5, EntryID: 9}, "/entries/9/photos/5"},
		{store.PhotoRef{PhotoID: 5, EntryID: 9, IsLedger: true}, "/ledger/9/photos/5"},
	} {
		if got := tc.ref.URL(); got != tc.want {
			t.Errorf("URL(%+v) = %q, want %q", tc.ref, got, tc.want)
		}
	}

	st, _, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		TaskLabel: "Photo", Unit: "h", Hours: dec("1"), HourlyRate: dec("10"), Cost: dec("10"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledgerID, err := st.CreateLedgerBooking(ctx, store.LedgerBookingInput{
		YearID: yearID, NeighborID: neighborID, Date: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), Incoming: true,
		IdempotencyKey: "photo-url-ledger", Booking: models.LedgerBooking{
			Version: 1, Kind: "fixed", TaskLabel: "Gegenleistung", Unit: "Pauschale", Quantity: dec("1"), UnitPrice: dec("20"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entryPhoto, err := st.AddEntryPhoto(ctx, entryID, []byte("entry-receipt"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	ledgerPhoto, err := st.AddLedgerPhoto(ctx, ledgerID, []byte("ledger-receipt"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh scratch database starts both identity sequences at 1: the two
	// photos share an id, which is exactly the collision the old URL mixed up.
	if entryPhoto != ledgerPhoto {
		t.Logf("photo ids differ (%d/%d); the URL check still applies", entryPhoto, ledgerPhoto)
	}
	refs, err := st.ListNeighborPhotos(ctx, yearID, neighborID)
	if err != nil || len(refs) != 2 {
		t.Fatalf("photos = %d (%v), want 2", len(refs), err)
	}
	for _, ref := range refs {
		want := "/entries/" + webIDStr(entryID) + "/photos/" + webIDStr(entryPhoto)
		if ref.IsLedger {
			want = "/ledger/" + webIDStr(ledgerID) + "/photos/" + webIDStr(ledgerPhoto)
		}
		if ref.URL() != want {
			t.Errorf("photo URL = %q, want %q", ref.URL(), want)
		}
	}
}

// TestIssueInvoiceConfirmedBindsContent pins WEB-07: the number is only issued
// for the content hash the operator confirmed.
func TestIssueInvoiceConfirmedBindsContent(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	preview, err := st.BuildInvoiceContent(ctx, yearID, neighborID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueInvoiceConfirmed(ctx, yearID, neighborID, 2026, time.Time{}, ""); !errors.Is(err, store.ErrInvoiceContentChanged) {
		t.Fatalf("empty hash = %v, want ErrInvoiceContentChanged", err)
	}
	// Another session adds a booking after the preview.
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		TaskLabel: "Late work", Unit: "Pauschale", Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(50), Cost: decimal.NewFromInt(50),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueInvoiceConfirmed(ctx, yearID, neighborID, 2026, time.Time{}, preview.Hash); !errors.Is(err, store.ErrInvoiceContentChanged) {
		t.Fatalf("stale hash = %v, want ErrInvoiceContentChanged", err)
	}
	if _, err := st.GetInvoice(ctx, yearID, neighborID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an invoice was issued for unconfirmed content: %v", err)
	}
	fresh, err := st.BuildInvoiceContent(ctx, yearID, neighborID)
	if err != nil {
		t.Fatal(err)
	}
	iv, err := st.IssueInvoiceConfirmed(ctx, yearID, neighborID, 2026, time.Time{}, fresh.Hash)
	if err != nil {
		t.Fatalf("confirmed issue: %v", err)
	}
	if iv.Content == nil || !iv.Content.Gross.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("issued content = %+v, want gross 150", iv.Content)
	}
	// A re-submit of the same confirmation is idempotent.
	again, err := st.IssueInvoiceConfirmed(ctx, yearID, neighborID, 2026, time.Time{}, fresh.Hash)
	if err != nil || again.ID != iv.ID {
		t.Fatalf("re-submit = %d (%v), want the existing %d", again.ID, err, iv.ID)
	}
}

// TestBankTargetsExcludeAnonymized pins WEB-04: an erased neighbor's invoice is
// no match or assignment target (booking on it is refused anyway).
func TestBankTargetsExcludeAnonymized(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	iv := issueFixtureInvoice(t, st, yearID, neighborID)
	if err := st.UpdateNeighbor(ctx, neighborID, "Review neighbor", "", "Review Road 2", "", "", "AT611904300234573201", nil); err != nil {
		t.Fatal(err)
	}
	has := func() (target, assignable, iban bool) {
		t.Helper()
		targets, err := st.IssuedInvoiceTargets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range targets {
			target = target || tg.ID == iv.ID
		}
		list, err := st.ListAssignableInvoices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			assignable = assignable || a.ID == iv.ID
		}
		ibans, err := st.NeighborIBANMap(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return target, assignable, ibans["AT611904300234573201"] == neighborID
	}
	if target, assignable, iban := has(); !target || !assignable || !iban {
		t.Fatalf("before erasure target=%v assignable=%v iban=%v, want all true", target, assignable, iban)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatal(err)
	}
	if target, assignable, iban := has(); target || assignable || iban {
		t.Fatalf("after erasure target=%v assignable=%v iban=%v, want all false", target, assignable, iban)
	}
}

// TestImportUploadsScopeAndExpiry pins WEB-03's server-side upload store: the
// content is returned only to its uploader, for its kind and year, and only
// until the TTL passes.
func TestImportUploadsScopeAndExpiry(t *testing.T) {
	st, pool, yearID, _ := scratchBookingFixture(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "upload-alice", "upload-password-long-1", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "upload-bob", "upload-password-long-2", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("Datum;Betrag;Verwendungszweck\n")
	payment, err := st.SaveImportUpload(ctx, store.ImportUploadPayment, alice, 0, content)
	if err != nil {
		t.Fatal(err)
	}
	booking, err := st.SaveImportUpload(ctx, store.ImportUploadBooking, alice, yearID, content)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadImportUpload(ctx, payment, store.ImportUploadPayment, alice, 0); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("own payment upload = %q (%v)", got, err)
	}
	if got, err := st.LoadImportUpload(ctx, booking, store.ImportUploadBooking, alice, yearID); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("own booking upload = %q (%v)", got, err)
	}
	for name, load := range map[string]func() error{
		"other user": func() error {
			_, err := st.LoadImportUpload(ctx, payment, store.ImportUploadPayment, bob, 0)
			return err
		},
		"other kind": func() error {
			_, err := st.LoadImportUpload(ctx, payment, store.ImportUploadBooking, alice, 0)
			return err
		},
		"other year": func() error {
			_, err := st.LoadImportUpload(ctx, booking, store.ImportUploadBooking, alice, yearID+1)
			return err
		},
		"no year": func() error {
			_, err := st.LoadImportUpload(ctx, booking, store.ImportUploadBooking, alice, 0)
			return err
		},
		"unknown": func() error {
			_, err := st.LoadImportUpload(ctx, "nope", store.ImportUploadPayment, alice, 0)
			return err
		},
	} {
		if err := load(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE import_uploads SET created_at = now() - interval '3 hours' WHERE token=$1`, payment); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadImportUpload(ctx, payment, store.ImportUploadPayment, alice, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired upload = %v, want ErrNotFound", err)
	}
	// The next upload purges it, and a user's surplus previews are trimmed.
	for i := 0; i < 12; i++ {
		if _, err := st.SaveImportUpload(ctx, store.ImportUploadPayment, bob, 0, content); err != nil {
			t.Fatal(err)
		}
	}
	var expired, bobs int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE token=$1), count(*) FILTER (WHERE user_id=$2) FROM import_uploads`, payment, bob).Scan(&expired, &bobs); err != nil {
		t.Fatal(err)
	}
	if expired != 0 || bobs != 10 {
		t.Fatalf("expired rows=%d bob's rows=%d, want 0 and 10", expired, bobs)
	}
	if _, err := st.SaveImportUpload(ctx, "other", alice, 0, content); err == nil {
		t.Fatal("an unknown upload kind was stored")
	}
	if _, err := st.SaveImportUpload(ctx, store.ImportUploadPayment, alice, 0, make([]byte, store.MaxImportUploadBytes+1)); err == nil {
		t.Fatal("oversized upload was stored")
	}
}

func webIDStr(n int64) string { return strconv.FormatInt(n, 10) }
