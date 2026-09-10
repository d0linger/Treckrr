//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

func TestListInvoiceDocsRebuildsOnlyLegacyInvoices(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: time.Now(),
		Hours: dec("2"), HourlyRate: dec("40"), Cost: dec("80"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"invoice", "storno", "gutschrift", "anzahlung"} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO invoices
			(billing_year_id, neighbor_id, number, issued_on, kind)
			VALUES ($1,$2,$3,current_date,$4)`, yearID, neighborID, "legacy-"+kind, kind); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := st.ListInvoiceDocs(ctx, yearID)
	if err != nil || len(docs) != 4 {
		t.Fatalf("documents: len=%d err=%v", len(docs), err)
	}
	for _, doc := range docs {
		t.Run(doc.Kind, func(t *testing.T) {
			if doc.Kind == "invoice" {
				if doc.Content == nil || !doc.Content.Net.Equal(dec("80")) {
					t.Fatalf("legacy invoice not rebuilt from bookings: %+v", doc.Content)
				}
				return
			}
			if doc.Content != nil {
				t.Fatalf("%s must not invent invoice content: %+v", doc.Kind, doc.Content)
			}
		})
	}
}
