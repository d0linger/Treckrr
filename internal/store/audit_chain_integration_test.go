package store_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// Verification must not mistake a concurrent seal or retention purge for tampering.
func TestAuditChainConcurrentMaintenanceIntegration(t *testing.T) {
	_, st := chainDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	maintenance := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if err := st.AddAudit(ctx, nil, "admin", "entry_create", "entry", "1", "concurrent", ""); err != nil {
				maintenance <- err
				return
			}
			if _, err := st.SealAuditLog(ctx); err != nil {
				maintenance <- err
				return
			}
			if _, err := st.PurgeAuditLog(ctx, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
				maintenance <- err
				return
			}
		}
		maintenance <- nil
	}()
	var verifyErr error
	for i := 0; i < 100; i++ {
		report, err := st.VerifyAuditChain(ctx)
		if err != nil || !report.OK() {
			verifyErr = fmt.Errorf("verification %d: %v, problems %v", i, err, report.Problems)
			break
		}
	}
	if err := <-maintenance; err != nil {
		t.Fatalf("concurrent maintenance: %v", err)
	}
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
}

// chainDatabase migrates a fresh, owned database: the chain spans the whole
// audit_log, and the shared test database has other tests deleting their rows.
func chainDatabase(t *testing.T) (*sql.DB, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("TEST_INVOICE_MIGRATION_DATABASES") != "1" {
		t.Skip("disposable databases require TEST_DATABASE_URL and TEST_INVOICE_MIGRATION_DATABASES=1")
	}
	ctx := context.Background()
	admin, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "treckrr_chain_" + hex.EncodeToString(token[:])
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil { // #nosec G202 -- generated hex name
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`"`); err != nil { // #nosec G202 -- generated hex name
			t.Errorf("drop %s: %v", name, err)
		}
		_ = admin.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, store.New(pool, "test-encryption-secret")
}

// TestAuditChainIntegration pins the tamper-evident chain: sealing links rows
// per retention segment, the verification passes on an intact log and names a
// changed or missing row, the purge removes a sealed prefix and re-anchors the
// chain, the guard admits only the sealing update, and heads copied from a
// restored backup must stay part of the live chain.
func TestAuditChainIntegration(t *testing.T) {
	pool, st := chainDatabase(t)
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	verify := func(wantOK bool, wantProblem string) store.AuditChainReport {
		t.Helper()
		report, err := st.VerifyAuditChain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if report.OK() != wantOK || (wantProblem != "" && !strings.Contains(strings.Join(report.Problems, "|"), wantProblem)) {
			t.Fatalf("verification ok=%v problems=%v; want ok=%v containing %q", report.OK(), report.Problems, wantOK, wantProblem)
		}
		return report
	}
	seal := func(want int) {
		t.Helper()
		if n, err := st.SealAuditLog(ctx); err != nil || n != want {
			t.Fatalf("sealed %d (%v), want %d", n, err, want)
		}
	}

	if due, err := st.AuditChainVerifyDue(ctx, 24*time.Hour); err != nil || !due {
		t.Fatalf("a never verified chain is not due: %v %v", due, err)
	}
	old := time.Now().AddDate(-8, 0, 0)
	for i := 0; i < 3; i++ {
		exec(`INSERT INTO audit_log (username, action, detail, created_at) VALUES ('alt', 'login', 'alt', $1)`, old)
		exec(`INSERT INTO audit_log (username, action, detail, created_at) VALUES ('alt', 'entry_update', 'alt', $1)`, old)
	}
	for _, action := range []string{"login", "entry_create", "logout", "invoice_issue"} {
		if err := st.AddAudit(ctx, nil, "admin", action, "entry", "1", "Detail "+action, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	seal(10)
	seal(0)
	if report := verify(true, ""); report.Checked != 10 {
		t.Fatalf("checked %d rows, want 10", report.Checked)
	}
	if due, _ := st.AuditChainVerifyDue(ctx, 24*time.Hour); due {
		t.Fatal("a fresh verification is still due")
	}

	// The guard admits the sealing update only: never a content change, never
	// a second sealing, never without the opt-in.
	var sealedID int64
	if err := pool.QueryRowContext(ctx, `SELECT id FROM audit_log WHERE detail='Detail entry_create'`).Scan(&sealedID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE audit_log SET detail='x' WHERE id=$1`,
		`UPDATE audit_log SET row_hash='\x00' WHERE id=$1`,
	} {
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_seal = 'on'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, query, sealedID); err == nil {
			t.Errorf("guard admitted %q on a sealed row", query)
		}
		_ = tx.Rollback()
	}

	// An edit below the application (owner disables the guard) is detected.
	tamper := func(query string, args ...any) {
		t.Helper()
		exec(`ALTER TABLE audit_log DISABLE TRIGGER audit_log_guard_trg`)
		defer exec(`ALTER TABLE audit_log ENABLE TRIGGER audit_log_guard_trg`)
		exec(query, args...)
	}
	tamper(`UPDATE audit_log SET detail='geschönt' WHERE id=$1`, sealedID)
	verify(false, "wurde verändert")
	tamper(`UPDATE audit_log SET detail='Detail entry_create' WHERE id=$1`, sealedID)
	verify(true, "")

	// Heads from a restored backup must stay in the live chain.
	heads, err := st.AuditChainHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range heads {
		exec(`INSERT INTO audit_chain_backup_heads (chain, seq, hash, source) VALUES ($1, $2, $3, 'Test')`, h.Chain, h.LastSeq, h.LastHash)
	}
	verify(true, "")

	// The purge removes the expired sealed prefix of both chains and re-anchors.
	n, err := st.PurgeAuditLog(ctx, time.Now().AddDate(0, -6, 0), time.Now().AddDate(-7, 0, 0))
	if err != nil || n != 6 {
		t.Fatalf("purged %d (%v), want the 6 old rows", n, err)
	}
	if err := st.AddAudit(ctx, nil, "admin", "entry_delete", "entry", "2", "nach dem Purge", ""); err != nil {
		t.Fatal(err)
	}
	seal(1)
	verify(true, "")
	heads, err = st.AuditChainHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range heads {
		if h.AnchorSeq != 3 {
			t.Errorf("chain %s anchored at %d, want 3", h.Chain, h.AnchorSeq)
		}
	}

	// A backup head the live chain no longer contains is reported.
	exec(`INSERT INTO audit_chain_backup_heads (chain, seq, hash, source) VALUES ('long', 5, '\x0102', 'Fremd')`)
	verify(false, "aus Sicherung Fremd")
	exec(`DELETE FROM audit_chain_backup_heads WHERE source='Fremd'`)

	// A row deleted from the middle (bypassing the purge) breaks the chain.
	tamper(`DELETE FROM audit_log WHERE id=$1`, sealedID)
	verify(false, "fehlt")
}
