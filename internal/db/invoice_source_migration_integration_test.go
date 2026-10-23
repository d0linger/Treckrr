//go:build integration

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Rehearse the exact shipped migration against historical synthetic schemas.
//
//go:embed migrations/0081_invoice_sources.sql
var invoiceSourceRehearsalSQL string

const invoiceSourceRehearsalName = "0081_invoice_sources.sql"

// TestInvoiceSourceMigrationPreservesHistory rehearses provenance metadata without
// changing the stored invoice, source values, payments, ledger or account balance.
func TestInvoiceSourceMigrationPreservesHistory(t *testing.T) {
	pool := invoiceSourceRehearsalDatabase(t)
	migrateBeforeInvoiceSources(t, pool)
	seedInvoiceSourceRehearsal(t, pool)
	before := invoiceSourceRehearsalSnapshot(t, pool)
	if err := applyMigrationOnce(t.Context(), pool, invoiceSourceRehearsalName, invoiceSourceRehearsalSQL); err != nil {
		t.Fatalf("rehearse candidate: %v", err)
	}
	if after := invoiceSourceRehearsalSnapshot(t, pool); after != before {
		t.Fatalf("existing financial rows changed:\nbefore: %s\nafter: %s", before, after)
	}
	var balance string
	if err := pool.QueryRowContext(t.Context(), `SELECT (
		(SELECT SUM(gross) FROM invoices WHERE kind='invoice' AND status='issued')
		+(SELECT SUM(amount) FROM neighbor_ledger WHERE NOT voided)
		-(SELECT SUM(amount) FROM payments WHERE deleted_at IS NULL))::text`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != "93.0000" {
		t.Fatalf("balance = %s, want 93.0000", balance)
	}
	var mapped, boundShares, selectedPayments int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM invoice_entries
		WHERE invoice_id=1 AND entry_id=1 AND source_entry_id=1 AND ordinal=1 AND active`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM beleg_shares WHERE invoice_id=1`).Scan(&boundShares); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM payments WHERE preferred_invoice_id IS NOT NULL`).Scan(&selectedPayments); err != nil {
		t.Fatal(err)
	}
	if mapped != 1 || boundShares != 1 || selectedPayments != 0 {
		t.Fatalf("mapping/share/payment preference = %d/%d/%d, want 1/1/0", mapped, boundShares, selectedPayments)
	}
	// A second document cannot claim a source already frozen by the first.
	rehearsalExec(t, pool, `INSERT INTO invoices (billing_year_id,neighbor_id,number,kind,status)
		VALUES (1,1,'2026-002','invoice','issued')`)
	if _, err := pool.ExecContext(t.Context(), `INSERT INTO invoice_entries
		(invoice_id,entry_id,source_entry_id,ordinal) VALUES (2,1,1,1)`); err == nil {
		t.Fatal("second active claim accepted for the same source")
	}
}

// TestInvoiceSourceMigrationRejectsAmbiguity proves failed rehearsal transactions
// leave both financial data and the complete old schema intact.
func TestInvoiceSourceMigrationRejectsAmbiguity(t *testing.T) {
	cases := []struct {
		name       string
		legacyCash bool
		mutate     string
	}{
		{"source cost mismatch", false, `UPDATE entries SET cost=101 WHERE id=1`},
		{"missing snapshot", false, `UPDATE invoices SET net=NULL WHERE id=1`},
		{"missing line quantity", false, `UPDATE invoices SET lines=lines #- '{0,qty}' WHERE id=1`},
		{"invalid line quantity", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,qty}','"invalid"') WHERE id=1`},
		{"malformed full line date", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,date}','"2026-05-01Tinvalid"') WHERE id=1`},
		{"invalid day end hour", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,date}','"2026-05-01T24:00:00Z"') WHERE id=1`},
		{"invalid leap second", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,date}','"2026-05-01T00:00:60Z"') WHERE id=1`},
		{"invalid timezone", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,date}','"2026-05-01T00:00:00+25:00"') WHERE id=1`},
		{"zero date with explicit offset", false, `UPDATE invoices SET lines=jsonb_set(lines,'{0,date}','"0001-01-01T00:00:00+00:00"') WHERE id=1`},
		{"zero invoice date", false, `UPDATE invoices SET issued_on='0001-01-01' WHERE id=1`},
		{"infinite invoice date", false, `UPDATE invoices SET issued_on='infinity' WHERE id=1`},
		{"zero payment date", false, `UPDATE payments SET paid_on='0001-01-01' WHERE id=1`},
		{"infinite payment date", false, `UPDATE payments SET paid_on='infinity' WHERE id=1`},
		{"zero credit date", false, `
			INSERT INTO invoices (billing_year_id,neighbor_id,number,kind,status,gross,references_invoice_id,issued_on)
			VALUES (1,1,'2026-G001','gutschrift','issued',-10,1,'0001-01-01')`},
		{"numeric label matching live text", false, `
			UPDATE entries SET task_label='42' WHERE id=1;
			UPDATE invoices SET lines=jsonb_set(lines,'{0,label}','42') WHERE id=1`},
		{"fractional historical cash", true, `UPDATE payments SET amount=25.0001 WHERE id=1`},
		{"deleted fractional historical cash", true, `UPDATE payments SET amount=25.0001,deleted_at=now() WHERE id=1`},
		{"fractional historical ledger", true, `UPDATE neighbor_ledger SET amount=5.0001 WHERE id=1`},
		{"voided fractional historical ledger", true, `UPDATE neighbor_ledger SET amount=5.0001,voided=true WHERE id=1`},
		{"canceling fractional ledger pair", true, `
			UPDATE neighbor_ledger SET amount=0.005 WHERE id=1;
			INSERT INTO neighbor_ledger(billing_year_id,neighbor_id,amount,description)
			VALUES(1,1,-0.005,'Synthetic offsetting legacy sub-cent row')`},
		{"wrong credit owner", false, `
			INSERT INTO neighbors (name) VALUES ('Other synthetic neighbor');
			INSERT INTO invoices (billing_year_id,neighbor_id,number,kind,status,gross,references_invoice_id)
			VALUES (1,2,'2026-G001','gutschrift','issued',-10,1)`},
		{"excess attached credit", false, `
			INSERT INTO invoices (billing_year_id,neighbor_id,number,kind,status,gross,references_invoice_id)
			VALUES (1,1,'2026-G001','gutschrift','issued',-114,1)`},
		{"partial reversal", false, `
			INSERT INTO payments (billing_year_id,neighbor_id,amount,paid_on,reversal_of_payment_id)
			VALUES (1,1,-24,'2026-05-03',1)`},
		{"ambiguous legacy share", false, `
			INSERT INTO invoices (billing_year_id,neighbor_id,number,kind,status,created_at)
			VALUES (1,1,'2026-000','invoice','canceled','2026-01-01')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := invoiceSourceRehearsalDatabase(t)
			if tc.legacyCash {
				migrateBeforeCentsGuards(t, pool)
			} else {
				migrateBeforeInvoiceSources(t, pool)
			}
			seedInvoiceSourceRehearsal(t, pool)
			rehearsalExec(t, pool, tc.mutate)
			if tc.legacyCash {
				// Reproduce legal pre-0062 values, then install normal guards.
				// No trigger or integrity check is disabled for this fixture.
				migrateBeforeInvoiceSources(t, pool)
			}
			before := invoiceSourceRehearsalSnapshot(t, pool)
			if err := applyMigrationOnce(t.Context(), pool, invoiceSourceRehearsalName, invoiceSourceRehearsalSQL); err == nil {
				t.Fatal("ambiguous historical fixture was accepted")
			}
			if after := invoiceSourceRehearsalSnapshot(t, pool); after != before {
				t.Fatalf("failed migration changed historical rows:\nbefore: %s\nafter: %s", before, after)
			}
			assertInvoiceSourceRehearsalRolledBack(t, pool)
		})
	}
}

// TestInvoiceSourceMigrationReissuedShares keeps historical capabilities separate
// from replacement invoices, including otherwise valid cancellation histories.
func TestInvoiceSourceMigrationReissuedShares(t *testing.T) {
	for _, tc := range []struct {
		name        string
		createdAt   string
		expired     bool
		wantFailure bool
		wantBinding int64
	}{
		{
			name: "share after reissue fails closed", createdAt: "2026-05-05",
			wantFailure: true,
		},
		{
			name: "expired share after reissue stays unbound", createdAt: "2026-05-05",
			expired: true,
		},
		{
			name: "share before cancellation stays with canceled original", createdAt: "2026-05-02T12:00:00Z",
			wantBinding: 1,
		},
		{
			name: "share before any invoice fails closed", createdAt: "2026-05-01",
			wantFailure: true,
		},
		{
			name: "share at replacement timestamp remains ambiguous", createdAt: "2026-05-04",
			wantFailure: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := invoiceSourceRehearsalDatabase(t)
			migrateBeforeInvoiceSources(t, pool)
			seedInvoiceSourceRehearsal(t, pool)
			seedInvoiceSourceReissue(t, pool)
			if _, err := pool.ExecContext(
				t.Context(),
				`UPDATE beleg_shares SET created_at=$1::timestamptz,
				 expires_at=now()+CASE WHEN $2 THEN -interval '1 day' ELSE interval '1 day' END`,
				tc.createdAt,
				tc.expired,
			); err != nil {
				t.Fatal(err)
			}
			before := invoiceSourceRehearsalSnapshot(t, pool)
			err := applyMigrationOnce(t.Context(), pool, invoiceSourceRehearsalName, invoiceSourceRehearsalSQL)
			if tc.wantFailure {
				if err == nil || !strings.Contains(err.Error(), "ambiguous unexpired legacy share") {
					t.Fatalf("expected legacy share rejection, got %v", err)
				}
				assertInvoiceSourceRehearsalRolledBack(t, pool)
			} else if err != nil {
				t.Fatalf("rehearse reissued history: %v", err)
			}
			if after := invoiceSourceRehearsalSnapshot(t, pool); after != before {
				t.Fatalf("migration changed historical rows:\nbefore: %s\nafter: %s", before, after)
			}
			if tc.wantFailure {
				return
			}
			var binding sql.NullInt64
			var activeTarget bool
			if err := pool.QueryRowContext(t.Context(), `SELECT s.invoice_id,
				COALESCE(i.kind='invoice' AND i.status='issued',false)
				FROM beleg_shares s LEFT JOIN invoices i ON i.id=s.invoice_id`).Scan(&binding, &activeTarget); err != nil {
				t.Fatal(err)
			}
			if binding.Valid != (tc.wantBinding != 0) || binding.Int64 != tc.wantBinding || activeTarget {
				t.Fatalf("share binding=%+v, active target=%t; want binding=%d and no replacement access",
					binding, activeTarget, tc.wantBinding)
			}
			var replacementClaims int
			if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM invoice_entries
				WHERE invoice_id=3 AND entry_id=1 AND active`).Scan(&replacementClaims); err != nil {
				t.Fatal(err)
			}
			if replacementClaims != 1 {
				t.Fatalf("replacement source claims=%d, want 1", replacementClaims)
			}
		})
	}
}

// seedInvoiceSourceReissue models the old single-active-invoice lifecycle without
// disabling constraints: retain A, cancel it with a reversal, then issue B.
func seedInvoiceSourceReissue(t *testing.T, pool *sql.DB) {
	t.Helper()
	rehearsalExec(t, pool, `
		INSERT INTO invoices(billing_year_id,neighbor_id,number,kind,status,references_invoice_id,
		                     issued_on,created_at,net,vat_rate,vat_amount,gross,show_vat,tax_mode,tax_note,
		                     service_from,service_to,issuer,recipient,lines,content_hash,payment_reference)
		SELECT billing_year_id,neighbor_id,number||'-S','storno','issued',id,
		       '2026-05-03','2026-05-03',-net,vat_rate,-vat_amount,-gross,show_vat,tax_mode,'Synthetic storno',
		       service_from,service_to,issuer,recipient,
		       (SELECT jsonb_agg(jsonb_set(l.value,'{cost}',to_jsonb(-(l.value->>'cost')::numeric)) ORDER BY l.ordinal)
		          FROM jsonb_array_elements(lines) WITH ORDINALITY AS l(value,ordinal)),
		       'synthetic-storno-hash',number||'-S'
		  FROM invoices WHERE id=1;
		UPDATE invoices SET status='canceled' WHERE id=1;
		INSERT INTO invoices(billing_year_id,neighbor_id,number,issued_on,created_at,
		                     net,vat_rate,vat_amount,gross,show_vat,tax_mode,tax_note,
		                     service_from,service_to,issuer,recipient,lines,content_hash,payment_reference)
		SELECT billing_year_id,neighbor_id,'2026-002','2026-05-04','2026-05-04',
		       net,vat_rate,vat_amount,gross,show_vat,tax_mode,tax_note,
		       service_from,service_to,issuer,recipient,lines,content_hash,'2026-002'
		  FROM invoices WHERE id=1;`)
}

// invoiceSourceRehearsalDatabase creates and owns a fresh database. The configured
// TEST_DATABASE_URL database is only the administrative connection, never a fixture.
func invoiceSourceRehearsalDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("TEST_INVOICE_MIGRATION_DATABASES") != "1" {
		t.Skip("disposable migration databases require TEST_DATABASE_URL and TEST_INVOICE_MIGRATION_DATABASES=1")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database configuration: %v", err)
	}
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "treckrr_invoice_rehearsal_" + hex.EncodeToString(token[:])
	if cfg.Database == name {
		t.Fatal("disposable database must differ from configured database")
	}
	admin := stdlib.OpenDB(*cfg)
	if err := admin.PingContext(t.Context()); err != nil {
		_ = admin.Close()
		t.Fatalf("connect to disposable test server: %v", err)
	}
	identifier := pgx.Identifier{name}.Sanitize()
	// The identifier is generated locally and quoted, never supplied by a caller.
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+identifier); err != nil {
		_ = admin.Close()
		t.Fatalf("create owned disposable database: %v", err)
	}
	child := cfg.Copy()
	child.Database = name
	pool := stdlib.OpenDB(*child)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close disposable database: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only the exact random name successfully created above is removed.
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("drop owned disposable database %s: %v", name, err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close administrative connection: %v", err)
		}
	})
	var connected string
	if err := pool.QueryRowContext(t.Context(), `SELECT current_database()`).Scan(&connected); err != nil {
		t.Fatal(err)
	}
	if connected != name {
		t.Fatalf("refuse fixture writes outside owned database: got %q", connected)
	}
	return pool
}

// migrateBeforeInvoiceSources applies the real prior migrations, never a toy schema.
func migrateBeforeInvoiceSources(t *testing.T, pool *sql.DB) {
	t.Helper()
	rehearsalExec(t, pool, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || file.Name() >= invoiceSourceRehearsalName {
			continue
		}
		var applied bool
		if err := pool.QueryRowContext(t.Context(),
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, file.Name()).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied {
			continue
		}
		content, err := migrationFS.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigrationOnce(t.Context(), pool, file.Name(), string(content)); err != nil {
			t.Fatal(err)
		}
	}
}

// migrateBeforeCentsGuards builds a real historical schema before inserting old
// precision values; Migrate subsequently installs every normal current safeguard.
func migrateBeforeCentsGuards(t *testing.T, pool *sql.DB) {
	t.Helper()
	rehearsalExec(t, pool, `CREATE TABLE schema_migrations (
		name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || file.Name() >= "0062_" {
			continue
		}
		content, err := migrationFS.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigrationOnce(t.Context(), pool, file.Name(), string(content)); err != nil {
			t.Fatal(err)
		}
	}
}

// seedInvoiceSourceRehearsal writes synthetic rows only to its caller-owned DB.
func seedInvoiceSourceRehearsal(t *testing.T, pool *sql.DB) {
	t.Helper()
	rehearsalExec(t, pool, `
		INSERT INTO price_bases(year,name) VALUES (2026,'Synthetic migration basis');
		INSERT INTO billing_years(year,base_id,label) VALUES(2026,1,'Synthetic migration year');
		INSERT INTO neighbors(name,address) VALUES('Synthetic migration neighbor','Synthetic address');
		INSERT INTO billing_year_neighbors(billing_year_id,neighbor_id) VALUES(1,1);
		INSERT INTO entries(neighbor_id,billing_year_id,entry_date,task_label,tractor_label,load_label,
		                    hours,hourly_rate,cost,unit,quantity,unit_price)
		VALUES(1,1,'2026-05-01','Synthetic work','','',0,0,100,'Pauschale',1,100);
		INSERT INTO invoices(billing_year_id,neighbor_id,number,issued_on,created_at,
		                     net,vat_rate,vat_amount,gross,show_vat,tax_mode,tax_note,
		                     service_from,service_to,issuer,recipient,lines,content_hash,payment_reference)
		VALUES(1,1,'2026-001','2026-05-02','2026-05-02',100,13,13,113,true,'pauschal','Synthetic tax notice',
		       '2026-05-01','2026-05-01','{"name":"Synthetic issuer","address":"Synthetic address"}',
		       '{"name":"Synthetic migration neighbor","address":"Synthetic address"}',
		       '[{"date":"2026-05-01T00:00:00Z","label":"Synthetic work","unit":"Pauschale", "qty":"1","unit_price":"100","cost":"100"}]',
		       'synthetic-frozen-hash','2026-001');
		INSERT INTO payments(billing_year_id,neighbor_id,amount,paid_on,invoice_id,note,method)
		VALUES(1,1,25,'2026-05-03',1,'Synthetic partial payment','bank');
		INSERT INTO neighbor_ledger(billing_year_id,neighbor_id,amount,posting_date,description)
		VALUES(1,1,5,'2026-05-03','Synthetic ledger adjustment');
		INSERT INTO beleg_shares(token_hash,neighbor_id,billing_year_id,expires_at,created_at)
		VALUES('synthetic-not-a-real-token',1,1,now()+interval '1 day','2026-05-04');`)
}

// invoiceSourceRehearsalSnapshot excludes only the new nullable/default metadata.
func invoiceSourceRehearsalSnapshot(t *testing.T, pool *sql.DB) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRowContext(t.Context(), `SELECT jsonb_build_object(
		'entries',(SELECT jsonb_agg(to_jsonb(e)-'price_term_label'-'price_term_pct'-'price_term_per_h'-'field_id'-'field_label'-'meter_start'-'meter_end' ORDER BY id) FROM entries e),
		'invoices',(SELECT jsonb_agg(to_jsonb(i)-'issue_request_hash'-'payment_term_days' ORDER BY id) FROM invoices i),
		'payments',(SELECT jsonb_agg(to_jsonb(p)-'preferred_invoice_id' ORDER BY id) FROM payments p),
		'ledger',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM neighbor_ledger l),
		'shares',(SELECT jsonb_agg(to_jsonb(s)-'invoice_id' ORDER BY id) FROM beleg_shares s),
		'sends',(SELECT jsonb_agg(to_jsonb(s)-'invoice_id' ORDER BY id) FROM beleg_sends s),
		'outbox',(SELECT jsonb_agg(to_jsonb(m)-'invoice_id' ORDER BY id) FROM mail_outbox m)
	)::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertInvoiceSourceRehearsalRolledBack(t *testing.T, pool *sql.DB) {
	t.Helper()
	var dirty bool
	if err := pool.QueryRowContext(t.Context(), `SELECT
		to_regclass('invoice_entries') IS NOT NULL
		OR to_regclass('invoices_active_per_neighbor_year') IS NULL
		OR EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)
		OR EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public'
		 AND ((table_name='invoices' AND column_name='issue_request_hash')
		 OR (table_name='payments' AND column_name='preferred_invoice_id')
		 OR (table_name IN ('beleg_shares','beleg_sends','mail_outbox') AND column_name='invoice_id')))`,
		invoiceSourceRehearsalName).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("migration failure left partial DDL or a schema version behind")
	}
	// Prove the former invariant remains enforced, not just that its index exists.
	_, err := pool.ExecContext(t.Context(), `INSERT INTO invoices
		(billing_year_id,neighbor_id,number) VALUES(1,1,'2026-rejected-duplicate')`)
	if err == nil || !strings.Contains(err.Error(), "invoices_active_per_neighbor_year") {
		t.Fatalf("old active-invoice uniqueness was not preserved: %v", err)
	}
}

func rehearsalExec(t *testing.T, pool *sql.DB, query string) {
	t.Helper()
	if _, err := pool.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}
