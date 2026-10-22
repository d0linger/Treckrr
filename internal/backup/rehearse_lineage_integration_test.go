//go:build integration

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
)

func lineageDatabase(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := db.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("treckrr_lineage_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *sql.DB
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Errorf("drop owned lineage fixture: %v", err)
		}
		admin.Close()
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err = db.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestRehearsalDoesNotAnchorDiscardedAuditHistoryIntegration(t *testing.T) {
	live, probe := lineageDatabase(t), lineageDatabase(t)
	ctx := t.Context()
	hash := bytes.Repeat([]byte{1}, 32)
	if _, err := live.ExecContext(ctx, `INSERT INTO audit_log(username,action,entity,entity_id,detail,ip,chain,chain_seq,prev_hash,row_hash)
		VALUES ('synthetic','test','test','','','','short',1,$1,$2)`, make([]byte, 32), hash); err != nil {
		t.Fatal(err)
	}
	if _, err := live.ExecContext(ctx, `UPDATE audit_chain_heads SET last_seq=1,last_hash=$1 WHERE chain='short'`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ExecContext(ctx, `UPDATE audit_chain_heads SET last_seq=CASE chain WHEN 'short' THEN 1 ELSE 2 END,last_hash=$1`, hash); err != nil {
		t.Fatal(err)
	}
	s := &Service{db: live}
	if err := s.recordAuditHeads(ctx, probe); err == nil {
		t.Fatal("discarded future was trusted")
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := live.QueryRowContext(ctx, `SELECT count(*) FROM audit_chain_backup_heads`).Scan(&n); err != nil || n != want {
			t.Fatalf("anchors=%d want=%d err=%v", n, want, err)
		}
	}
	count(0) // even a matching earlier chain must roll back
	if _, err := probe.ExecContext(ctx, `UPDATE audit_chain_heads SET last_seq=0 WHERE chain='long'`); err != nil {
		t.Fatal(err)
	}
	if err := s.recordAuditHeads(ctx, probe); err != nil {
		t.Fatal(err)
	}
	count(1)
	if _, err := probe.ExecContext(ctx, `UPDATE audit_chain_heads SET last_hash=$1 WHERE chain='short'`, bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.recordAuditHeads(ctx, probe); err == nil {
		t.Fatal("diverged history was trusted")
	}
	count(1)
}
