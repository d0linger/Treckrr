//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestCreditReversalRevenueIntegration(t *testing.T) {
	for _, attached := range []bool{false, true} {
		name := "free"
		if attached {
			name = "attached"
		}
		t.Run(name, func(t *testing.T) {
			st, pool, yearID, neighborID := scratchBookingFixture(t)
			ctx := context.Background()
			if err := st.UpdateCompany(ctx, models.Company{
				Name: "Test farm", Address: "Test road 1", TaxMode: "kleinunternehmer",
				TaxNote: "Kleinunternehmer",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.ExecContext(ctx, `UPDATE neighbors SET address='Test road 2' WHERE id=$1`, neighborID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateEntry(ctx, &models.Entry{
				NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 1, 2),
				TaskLabel: "Test work", Unit: "ha", Quantity: dec("1"), UnitPrice: dec("100"), Cost: dec("100"),
			}, nil); err != nil {
				t.Fatal(err)
			}
			iv, err := st.IssueInvoice(ctx, yearID, neighborID, 2026, day(2026, 1, 3))
			if err != nil {
				t.Fatal(err)
			}
			var credit models.Invoice
			if attached {
				credit, err = st.GutschriftInvoice(ctx, yearID, neighborID, dec("20"), "Test credit")
			} else {
				credit, err = st.FreeGutschrift(ctx, yearID, neighborID, 2026, dec("20"), "Test credit")
			}
			if err != nil {
				t.Fatal(err)
			}
			reversal, err := st.StornoDocument(ctx, credit.ID, "Test reversal")
			if err != nil {
				t.Fatal(err)
			}
			// The isolated synthetic documents cross calendar years; changing
			// only their dates exercises both journal and calendar SQL accounting.
			if _, err := pool.ExecContext(ctx, `UPDATE invoices SET issued_on=$1 WHERE id=ANY($2)`,
				day(2026, 12, 31), []int64{iv.ID, credit.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.ExecContext(ctx, `UPDATE invoices SET issued_on=$1 WHERE id=$2`,
				day(2027, 1, 1), reversal.ID); err != nil {
				t.Fatal(err)
			}
			journal, err := st.ListInvoiceJournal(ctx, yearID)
			if err != nil {
				t.Fatal(err)
			}
			total := decimal.Zero
			for _, row := range journal {
				if row.CountsForRevenue() {
					total = total.Add(row.Gross)
				}
			}
			if !total.Equal(dec("100")) {
				t.Fatalf("journal gross = %s, want 100", total)
			}
			for calYear, want := range map[int]string{2026: "80", 2027: "20"} {
				got, err := st.KUCalendarYearGross(ctx, calYear)
				if err != nil || !got.Equal(dec(want)) {
					t.Fatalf("calendar %d = %s (%v), want %s", calYear, got, err, want)
				}
			}
			if attached {
				// A still-issued credit canceled by the full invoice cascade must
				// drop out; the already reversed credit must remain with its pair.
				if _, err := st.GutschriftInvoice(ctx, yearID, neighborID, dec("10"), "Cascade credit"); err != nil {
					t.Fatal(err)
				}
				if _, err := st.StornoInvoice(ctx, yearID, neighborID, "Full reversal"); err != nil {
					t.Fatal(err)
				}
				journal, err = st.ListInvoiceJournal(ctx, yearID)
				if err != nil {
					t.Fatal(err)
				}
				total = decimal.Zero
				for _, row := range journal {
					if row.CountsForRevenue() {
						total = total.Add(row.Gross)
					}
				}
				if !total.IsZero() {
					t.Fatalf("after invoice reversal gross = %s, want 0", total)
				}
			}
		})
	}
}
