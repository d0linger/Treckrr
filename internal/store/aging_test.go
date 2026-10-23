package store

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestAgingFromRows buckets open remainders by days past due, ignores settled
// invoices and lays the bar segments out side by side.
func TestAgingFromRows(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	day := func(daysBefore int) time.Time {
		return time.Date(2026, 10, 20-daysBefore, 0, 0, 0, 0, time.UTC)
	}
	d := decimal.RequireFromString
	rows := []DunningRow{
		{DueOn: asOf.AddDate(0, 0, 5), Open: d("100")}, // not yet due
		{DueOn: day(0), Open: d("10")},                 // due today, not overdue
		{DueOn: day(1), Open: d("20")},
		{DueOn: day(30), Open: d("30")},
		{DueOn: day(31), Open: d("40")},
		{DueOn: day(60), Open: d("50")},
		{DueOn: day(61), Open: d("200")},
		{DueOn: day(90), Open: decimal.Zero}, // settled
	}
	got := agingFromRows(rows, asOf)
	want := []struct {
		amount   string
		invoices int
	}{{"110", 2}, {"50", 2}, {"90", 2}, {"200", 1}}
	for i, w := range want {
		if !got.Buckets[i].Amount.Equal(d(w.amount)) || got.Buckets[i].Invoices != w.invoices {
			t.Errorf("bucket %q: %s/%d, want %s/%d", got.Buckets[i].Label, got.Buckets[i].Amount, got.Buckets[i].Invoices, w.amount, w.invoices)
		}
	}
	if !got.Total.Equal(d("450")) {
		t.Fatalf("total %s, want 450", got.Total)
	}
	segments := got.Segments()
	if len(segments) != 4 || segments[0].X != "0.00" || segments[3].Width != "44.44" || segments[3].Tone != "bad" {
		t.Fatalf("segments %+v", segments)
	}
	if len(agingFromRows(nil, asOf).Segments()) != 0 {
		t.Fatal("no open amount must draw no bar")
	}
}
