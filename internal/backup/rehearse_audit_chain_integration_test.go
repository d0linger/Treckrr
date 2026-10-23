package backup_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestRehearsalRecordsAuditChainHeadsIntegration pins the backup side of the
// audit chain (0091): a rehearsal copies the chain heads of the restored
// backup into the live database, where the verification checks them. It runs
// on its own database because sealing the shared one would race other tests.
func TestRehearsalRecordsAuditChainHeadsIntegration(t *testing.T) {
	requirePGTools(t)
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
	name := "treckrr_chainbackup_" + hex.EncodeToString(token[:])
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
	st := store.New(pool, "rehearsal-test-encryption-secret")
	for _, action := range []string{"login", "entry_create"} {
		if err := st.AddAudit(ctx, nil, "system", action, "test", "", "rehearsal chain head", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SealAuditLog(ctx); err != nil {
		t.Fatal(err)
	}
	heads, err := st.AuditChainHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}

	svc := backup.New(backup.Options{
		DatabaseURL: u.String(),
		RehearseURL: dsn, // same server, separate scratch database
		EncKey:      "rehearsal-test-key-at-least-32-bytes!!",
		Dir:         t.TempDir(),
	}, pool)
	enc, _, err := svc.CreateEncrypted(ctx)
	if err != nil {
		t.Fatalf("create dump: %v", err)
	}
	if _, err := svc.RehearseRestore(ctx, enc); err != nil {
		t.Fatalf("rehearsal: %v", err)
	}
	for _, h := range heads {
		var recorded bool
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_chain_backup_heads WHERE chain=$1 AND seq=$2 AND hash=$3)`,
			h.Chain, h.LastSeq, h.LastHash).Scan(&recorded); err != nil || !recorded {
			t.Errorf("rehearsal did not record the %s chain head %d: %v", h.Chain, h.LastSeq, err)
		}
	}
	if report, err := st.VerifyAuditChain(ctx); err != nil || !report.OK() {
		t.Errorf("verification after the rehearsal = %+v, %v", report, err)
	}
}
