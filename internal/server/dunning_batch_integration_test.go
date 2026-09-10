//go:build integration

package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestDunningBatchContinuesAfterInvalidSnapshotIntegration(t *testing.T) {
	e := newItEnv(t)
	var ids []int64
	for _, label := range []string{"A", "B", "C"} {
		id, err := e.st.CreateNeighbor(e.ctx, label+" "+e.uname, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		e.post(fmt.Sprintf("/neighbors/%d/update", id), url.Values{
			"name": {label + " " + e.uname}, "email": {"batch@example.invalid"}, "payment_term_days": {"0"},
		})
		// Seed synthetic snapshots directly so B has valid JSON of the wrong
		// shape. DunningRows can list it; GetInvoice must reject its content.
		lines := "[]"
		if label == "B" {
			lines = "{}"
		}
		_, err = e.pool.ExecContext(e.ctx, `INSERT INTO invoices
			(billing_year_id,neighbor_id,number,issued_on,net,gross,issuer,recipient,lines)
			VALUES($1,$2,$3,CURRENT_DATE-1,100,100,'{}','{}',$4::jsonb)`,
			e.yearID64, id, "batch-"+label+"-"+e.uname, lines)
		if err != nil {
			t.Fatal(err)
		}
	}
	body := e.post("/mahnwesen/batch-email", url.Values{"year": {itoa64(e.yearID64)}, "stufe": {"1"}})
	for _, want := range []string{"2 zur Wiederholung eingeplant", "1 fehlgeschlagen", "nur betroffene Nachbarn"} {
		if !strings.Contains(body, want) {
			t.Errorf("batch summary missing %q", want)
		}
	}
	for i, id := range ids {
		var count int
		if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM mail_outbox WHERE neighbor_id=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 1 {
			want = 0
		}
		if count != want {
			t.Errorf("neighbor %d queued %d times, want %d", id, count, want)
		}
	}
}
