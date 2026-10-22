package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
)

// AgingBucket is the open amount of issued invoices within one age range.
type AgingBucket struct {
	Label    string
	Tone     string // ok, warn, bad
	Amount   decimal.Decimal
	Invoices int
}

// InvoiceAging groups the open remainder of every issued invoice, across all
// billing years, by how far past its due date it is.
type InvoiceAging struct {
	Buckets []AgingBucket
	Total   decimal.Decimal
}

// AgingSegment is one drawn part of the stacked bar, in percent of the total.
type AgingSegment struct {
	Label, Tone string
	Amount      decimal.Decimal
	X, Width    string
}

// Segments lays the non-empty buckets side by side on a 0-100 scale.
func (a InvoiceAging) Segments() []AgingSegment {
	if !a.Total.IsPositive() {
		return nil
	}
	hundred := decimal.NewFromInt(100)
	var out []AgingSegment
	x := decimal.Zero
	for _, b := range a.Buckets {
		if !b.Amount.IsPositive() {
			continue
		}
		w := b.Amount.Div(a.Total).Mul(hundred)
		out = append(out, AgingSegment{Label: b.Label, Tone: b.Tone, Amount: b.Amount,
			X: x.StringFixed(2), Width: w.StringFixed(2)})
		x = x.Add(w)
	}
	return out
}

// ReadInvoiceAging uses the same terms and allocations as the Mahnwesen list,
// so the bar and the list never disagree. Read-only.
func (s *Store) ReadInvoiceAging(ctx context.Context, termDays int, asOf time.Time) (InvoiceAging, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return InvoiceAging{}, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only; no-op after Commit
	rows, err := invoiceOpenRows(ctx, tx, 0, termDays)
	if err != nil {
		return InvoiceAging{}, err
	}
	return agingFromRows(rows, asOf), tx.Commit()
}

// agingFromRows sorts open invoice remainders into age buckets as of a date.
func agingFromRows(rows []DunningRow, asOf time.Time) InvoiceAging {
	out := InvoiceAging{Buckets: []AgingBucket{
		{Label: "noch nicht fällig", Tone: "ok"},
		{Label: "1–30 Tage überfällig", Tone: "warn"},
		{Label: "31–60 Tage überfällig", Tone: "warn"},
		{Label: "über 60 Tage überfällig", Tone: "bad"},
	}}
	for _, row := range rows {
		if !row.Open.IsPositive() {
			continue
		}
		i := 0
		if days := calc.DaysBetween(row.DueOn, asOf); days > 0 {
			switch {
			case days <= 30:
				i = 1
			case days <= 60:
				i = 2
			default:
				i = 3
			}
		}
		out.Buckets[i].Amount = out.Buckets[i].Amount.Add(row.Open)
		out.Buckets[i].Invoices++
		out.Total = out.Total.Add(row.Open)
	}
	return out
}
