//go:build integration

package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

const billingUpgradeDatabase = "treckrr_parity_upgrade"

var billingUpgradeNames = []string{
	"Synthetic upgrade A credited",
	"Synthetic upgrade B ledger residual",
	"Synthetic upgrade C paid and sent",
	"Synthetic upgrade D unbilled",
}

// TestBillingUpgradeParityIntegration is a two-binary, same-database probe.
// Run seed with the pre-0081 revision, then verify with the new revision against
// the exact same disposable database. The owner must provide a network-isolated
// PostgreSQL container and remove it afterwards. This test never drops data and
// never uses the ordinary scratch fixtures or an application's database.
func TestBillingUpgradeParityIntegration(t *testing.T) {
	phase := os.Getenv("TEST_BILLING_UPGRADE_PHASE")
	if phase == "" {
		t.Skip("opt-in old/new upgrade probe; requires an explicitly isolated treckrr_parity_upgrade database")
	}
	if phase != "seed" && phase != "verify" {
		t.Fatal("TEST_BILLING_UPGRADE_PHASE must be seed or verify")
	}
	if os.Getenv("TEST_BILLING_UPGRADE_ALLOWED") != "1" {
		t.Fatal("upgrade probe requires TEST_BILLING_UPGRADE_ALLOWED=1")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid upgrade probe database configuration")
	}
	loopback := func(host string) bool { return host == "127.0.0.1" || host == "::1" }
	if dsn == "" || config.Database != billingUpgradeDatabase || !loopback(config.Host) {
		t.Fatal("upgrade probe requires the exact treckrr_parity_upgrade database on loopback")
	}
	for _, fallback := range config.Fallbacks {
		if !loopback(fallback.Host) {
			t.Fatal("upgrade probe refuses a non-loopback connection fallback")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect to isolated upgrade database failed")
	}
	t.Cleanup(func() { _ = pool.Close() })
	var actualDatabase string
	if err := pool.QueryRowContext(ctx, `SELECT current_database()`).Scan(&actualDatabase); err != nil {
		t.Fatal(err)
	}
	if actualDatabase != billingUpgradeDatabase {
		t.Fatal("connected database does not match the isolated upgrade fixture")
	}
	st := store.New(pool, "synthetic-upgrade-parity-encryption-secret")
	if phase == "seed" {
		var occupied bool
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname NOT IN ('pg_catalog','information_schema')
			AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','v','m','S','f'))`).Scan(&occupied); err != nil {
			t.Fatal(err)
		}
		if occupied {
			t.Fatal("seed refuses a database containing user relations; provide a fresh disposable database")
		}
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatalf("seed migrations: %v", err)
		}
		billingUpgradeRequireLegacy(t, ctx, pool)
		billingUpgradeSeed(t, ctx, st)
	} else {
		billingUpgradeRequireLegacy(t, ctx, pool)
		billingUpgradeFixture(t, ctx, pool)
		before := billingUpgradeRows(t, ctx, pool)
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatalf("upgrade migrations: %v", err)
		}
		billingUpgradeNeutralDefaults(t, ctx, pool)
		if changed, err := st.BackfillInvoiceSnapshots(ctx); err != nil || changed != 0 {
			t.Fatalf("startup backfill: changed=%d err=%v, want no reconstructed snapshots", changed, err)
		}
		after := billingUpgradeRows(t, ctx, pool)
		for table, previous := range before {
			if !bytes.Equal(previous, after[table]) {
				t.Errorf("upgrade changed existing columns in %s", table)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
		var claims, active int
		if err := pool.QueryRowContext(ctx,
			`SELECT count(*),count(*) FILTER(WHERE active) FROM invoice_entries`).Scan(&claims, &active); err != nil {
			t.Fatal(err)
		}
		if claims != 3 || active != 3 {
			t.Fatalf("upgrade source claims=%d/%d, want exactly three active invoice sources", claims, active)
		}
	}
	// Both binaries emit the same report shape. Financial assertions are strict;
	// dunning and closing remain raw observations so semantic differences cannot
	// be hidden by adapting expected values to the new implementation.
	report := billingUpgradeReport(t, ctx, st, pool)
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("BILLING_UPGRADE %s", body)
}

func billingUpgradeRequireLegacy(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	var last string
	if err := pool.QueryRowContext(ctx, `SELECT COALESCE(max(name),'') FROM schema_migrations`).Scan(&last); err != nil {
		t.Fatalf("read legacy migration state: %v", err)
	}
	if !strings.HasPrefix(last, "0080_") {
		t.Fatalf("probe requires the genuine pre-0081 schema, last migration=%q", last)
	}
	var assigned bool
	if err := pool.QueryRowContext(ctx, `SELECT to_regclass('invoice_entries') IS NOT NULL`).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned {
		t.Fatal("source assignments already exist; verify must start from the seeded old database")
	}
}

func billingUpgradeSeed(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	baseID, err := st.CreateEmptyBase(ctx, 2026, "Synthetic upgrade parity base")
	if err != nil {
		t.Fatal(err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2026, baseID, "Synthetic upgrade parity year")
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	var creditedID int64
	for i, fixture := range []struct {
		mode, rate, net, ledger, payment string
	}{
		{"regel", "20", "100", "-10", "25"},
		{"kleinunternehmer", "0", "100", "20", "110"},
		{"pauschal", "13", "75", "0", "84.75"},
		{"kleinunternehmer", "0", "9.995", "-2", "1"},
	} {
		company := models.Company{
			Name: "Synthetic upgrade parity farm", Address: "Synthetic road 1",
			TaxID: "ATU12345678", TaxNote: "Synthetic test tax notice",
			TaxMode: fixture.mode, VATRate: decimal.RequireFromString(fixture.rate),
			InvoiceStart: 1, PaymentTermDays: 14,
		}
		if err := st.UpdateCompany(ctx, company); err != nil {
			t.Fatal(err)
		}
		neighborID, err := st.CreateNeighbor(ctx, billingUpgradeNames[i], "Synthetic upgrade parity fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateNeighbor(ctx, neighborID, billingUpgradeNames[i], "Synthetic upgrade parity fixture",
			"Synthetic recipient road 2", "", "", "", nil); err != nil {
			t.Fatal(err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, neighborID); err != nil {
			t.Fatal(err)
		}
		amount := decimal.RequireFromString(fixture.net)
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: neighborID, BillingYearID: yearID, Date: date,
			TaskLabel: "Synthetic upgrade work", Unit: "Pauschale",
			Quantity: decimal.NewFromInt(1), UnitPrice: amount, Cost: amount,
		}, nil); err != nil {
			t.Fatal(err)
		}
		if fixture.ledger != "0" {
			if _, err := st.AddNeighborLedger(ctx, yearID, neighborID, decimal.RequireFromString(fixture.ledger),
				"Synthetic upgrade ledger", date); err != nil {
				t.Fatal(err)
			}
		}
		if i < 3 {
			invoice, err := st.IssueInvoice(ctx, yearID, neighborID, 2026, date)
			if err != nil || invoice.Content == nil {
				t.Fatalf("seed invoice %d: %v", i, err)
			}
		}
		if err := st.AddPayment(ctx, yearID, neighborID, decimal.RequireFromString(fixture.payment),
			date.AddDate(0, 0, 1), "Synthetic upgrade payment", "bank"); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			creditedID = neighborID
		}
		if i == 2 {
			if err := st.RecordBelegSend(ctx, yearID, neighborID, "manuell"); err != nil {
				t.Fatal(err)
			}
		}
		if i == 3 {
			entryID, err := st.CreateEntry(ctx, &models.Entry{
				NeighborID: neighborID, BillingYearID: yearID, Date: date,
				TaskLabel: "Synthetic voided work", Unit: "Pauschale",
				Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(30), Cost: decimal.NewFromInt(30),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SetEntryVoided(ctx, entryID, true, "Synthetic void fixture"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Create the credit last: its real issue date must not precede later invoice
	// numbers, and its VAT must come from A's frozen 20%, not current company KU.
	if _, err := st.GutschriftInvoice(ctx, yearID, creditedID, decimal.NewFromInt(12), "Synthetic upgrade credit"); err != nil {
		t.Fatal(err)
	}
}

func billingUpgradeFixture(t *testing.T, ctx context.Context, pool *sql.DB) (int64, map[string]int64) {
	t.Helper()
	rows, err := pool.QueryContext(ctx, `SELECT id,name FROM neighbors ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := make([]string, 0, len(billingUpgradeNames))
	ids := make(map[string]int64)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		names, ids[name] = append(names, name), id
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, billingUpgradeNames) {
		t.Fatal("verify refuses a database without exactly the four expected synthetic neighbors")
	}
	var yearID int64
	var years, members, entries, invoices, payments, ledger, sends int
	if err := pool.QueryRowContext(ctx, `SELECT
		(SELECT min(id) FROM billing_years WHERE year=2026 AND label='Synthetic upgrade parity year'),
		(SELECT count(*) FROM billing_years), (SELECT count(*) FROM billing_year_neighbors),
		(SELECT count(*) FROM entries), (SELECT count(*) FROM invoices),
		(SELECT count(*) FROM payments), (SELECT count(*) FROM neighbor_ledger),
		(SELECT count(*) FROM beleg_sends)`).Scan(&yearID, &years, &members, &entries, &invoices, &payments, &ledger, &sends); err != nil {
		t.Fatal(err)
	}
	if years != 1 || members != 4 || entries != 5 || invoices != 4 || payments != 4 || ledger != 3 || sends != 1 {
		t.Fatalf("unexpected upgrade fixture sizes: years=%d members=%d entries=%d documents=%d cash=%d ledger=%d sends=%d",
			years, members, entries, invoices, payments, ledger, sends)
	}
	return yearID, ids
}

func billingUpgradeRows(t *testing.T, ctx context.Context, pool *sql.DB) map[string]json.RawMessage {
	t.Helper()
	// Static SQL only. Keep every old column, including payments.invoice_id,
	// IDs, timestamps, snapshot hashes, individual line decimals and audit details.
	// Project only the metadata added since the baseline out of the comparison;
	// billingUpgradeNeutralDefaults checks those new fields independently.
	tables := []string{
		"company", "price_bases", "billing_years", "neighbors", "billing_year_neighbors",
		"entries", "entry_machines", "neighbor_ledger", "audit_log", "invoices", "payments",
		"beleg_sends", "beleg_shares", "mail_outbox",
	}
	out := make(map[string]json.RawMessage, len(tables))
	for _, table := range tables {
		out[table] = json.RawMessage(`[]`)
	}
	rows, err := pool.QueryContext(ctx, `SELECT table_name,jsonb_agg(row_data ORDER BY row_data::text) FROM (
		SELECT 'company' AS table_name,to_jsonb(r) AS row_data FROM company r
		UNION ALL SELECT 'price_bases',to_jsonb(r)-'billing_increment_h'-'minimum_h' FROM price_bases r
		UNION ALL SELECT 'billing_years',to_jsonb(r) FROM billing_years r
		UNION ALL SELECT 'neighbors',to_jsonb(r) FROM neighbors r
		UNION ALL SELECT 'billing_year_neighbors',to_jsonb(r) FROM billing_year_neighbors r
		UNION ALL SELECT 'entries',to_jsonb(r)-'price_term_label'-'price_term_pct'-'price_term_per_h'
			-'field_id'-'field_label'-'meter_start'-'meter_end' FROM entries r
		UNION ALL SELECT 'entry_machines',to_jsonb(r) FROM entry_machines r
		UNION ALL SELECT 'neighbor_ledger',to_jsonb(r) FROM neighbor_ledger r
		UNION ALL SELECT 'audit_log',to_jsonb(r)-'chain'-'chain_seq'-'prev_hash'-'row_hash' FROM audit_log r
		UNION ALL SELECT 'invoices',to_jsonb(r)-'issue_request_hash'-'payment_term_days' FROM invoices r
		UNION ALL SELECT 'payments',to_jsonb(r)-'preferred_invoice_id' FROM payments r
		UNION ALL SELECT 'beleg_sends',to_jsonb(r)-'invoice_id' FROM beleg_sends r
		UNION ALL SELECT 'beleg_shares',to_jsonb(r)-'invoice_id' FROM beleg_shares r
		UNION ALL SELECT 'mail_outbox',to_jsonb(r)-'invoice_id' FROM mail_outbox r
	) AS preserved GROUP BY table_name ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var rowJSON []byte
		if err := rows.Scan(&table, &rowJSON); err != nil {
			t.Fatal(err)
		}
		out[table] = json.RawMessage(rowJSON)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func billingUpgradeNeutralDefaults(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	// The old fixture has no billing rules, terms, fields, meter readings, MFA
	// requirement or audit seals. An upgrade must not introduce any of them.
	var changed bool
	if err := pool.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM price_bases WHERE billing_increment_h <> 0 OR minimum_h <> 0)
		OR EXISTS(SELECT 1 FROM entries WHERE price_term_label <> '' OR price_term_pct <> 0
			OR price_term_per_h <> 0 OR field_id IS NOT NULL OR field_label <> ''
			OR meter_start IS NOT NULL OR meter_end IS NOT NULL)
		OR EXISTS(SELECT 1 FROM neighbor_price_terms)
		OR EXISTS(SELECT 1 FROM neighbor_fields)
		OR EXISTS(SELECT 1 FROM entry_signatures)
		OR EXISTS(SELECT 1 FROM accounting_export_profiles WHERE format <> 'custom')
		OR EXISTS(SELECT 1 FROM security_policy WHERE require_mfa_admin OR require_mfa_editor)
		OR EXISTS(SELECT 1 FROM audit_log WHERE chain IS NOT NULL OR chain_seq IS NOT NULL
			OR prev_hash IS NOT NULL OR row_hash IS NOT NULL)`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("upgrade introduced non-neutral defaults into historical data")
	}
}

func billingUpgradeReport(t *testing.T, ctx context.Context, st *store.Store, pool *sql.DB) map[string]any {
	t.Helper()
	yearID, ids := billingUpgradeFixture(t, ctx, pool)
	accounts := make(map[string]any, len(ids))
	wants := []string{"73", "10", "0", "7"}
	for i, name := range billingUpgradeNames {
		id := ids[name]
		remaining, err := st.AccountRemaining(ctx, yearID, id)
		if err != nil || !remaining.Equal(decimal.RequireFromString(wants[i])) {
			t.Fatalf("%s annual remaining=%s err=%v, want %s", name, remaining, err, wants[i])
		}
		invoiceRemaining, err := st.InvoiceRemaining(ctx, yearID, id)
		if err != nil {
			t.Fatal(err)
		}
		history, err := st.NeighborYearHistory(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		accounts[name] = map[string]any{"remaining": remaining, "invoice_remaining": invoiceRemaining, "history": history}
	}
	summaries, err := st.YearNeighborSummaries(ctx, yearID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := st.ListInvoiceJournal(ctx, yearID)
	if err != nil {
		t.Fatal(err)
	}
	paid, open, credit, err := st.YearPaymentTotals(ctx, yearID)
	if err != nil || !paid.Equal(decimal.RequireFromString("220.75")) ||
		!open.Equal(decimal.RequireFromString("89.995")) || !credit.IsZero() {
		t.Fatalf("year cash/open/credit=%s/%s/%s err=%v", paid, open, credit, err)
	}
	dunning, err := st.DunningRows(ctx, yearID, 14, time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// InvoiceID was added by 0081. Project the unchanged public fields explicitly
	// without changing any reported open amount or hiding missing dunning rows.
	dunningRows := make([]map[string]any, 0, len(dunning))
	for _, row := range dunning {
		dunningRows = append(dunningRows, map[string]any{
			"year_id": row.YearID, "year": row.YearNo, "neighbor_id": row.NeighborID, "name": row.Name,
			"invoice": row.InvoiceNo, "issued_on": row.IssuedOn, "term_days": row.TermDays,
			"due_on": row.DueOn, "days_overdue": row.DaysOverdue, "open": row.Open,
		})
	}
	closing, err := st.YearClosingChecks(ctx, yearID)
	if err != nil {
		t.Fatal(err)
	}
	closingRows := make(map[string]any, len(closing))
	for _, row := range closing {
		closingRows[row.Key] = map[string]any{
			"count": row.Count, "amount": row.Amount, "names": row.Names, "neighbors": row.Neighbors,
		}
	}
	return map[string]any{
		"fixture": "synthetic-pre0081-upgrade-v1", "financial_rows": billingUpgradeRows(t, ctx, pool),
		"accounts": accounts, "summaries": summaries, "journal": journal,
		"year_payments": map[string]any{"paid": paid, "open": open, "credit": credit},
		"dunning":       dunningRows, "closing": closingRows,
	}
}
