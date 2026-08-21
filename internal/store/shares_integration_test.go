package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestBelegShareIntegration proves the self-service share lifecycle: create with
// a validity, resolve by hash, revoke (idempotent), and expiry — plus neighbor
// scoping on revoke. Runs only when TEST_DATABASE_URL is set.
func TestBelegShareIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	nano := time.Now().UnixNano()
	yr := int(nano % 2_000_000_000)
	baseID, err := st.CreateEmptyBase(ctx, yr, "Share-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Share-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Share%d", nano), "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}

	// create + resolve
	hash := fmt.Sprintf("hash-%d", nano)
	id, err := st.CreateBelegShare(ctx, hash, nid, yearID, time.Now().Add(24*time.Hour), "admin")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gotN, gotY, ok, err := st.ResolveBelegShare(ctx, hash)
	if err != nil || !ok || gotN != nid || gotY != yearID {
		t.Fatalf("resolve: got (%d,%d,%v,%v), want (%d,%d,true,nil)", gotN, gotY, ok, err, nid, yearID)
	}
	shares, err := st.ListBelegShares(ctx, nid, yearID)
	if err != nil || len(shares) != 1 || shares[0].ID != id {
		t.Fatalf("list: %v / %+v", err, shares)
	}
	if shares[0].LastUsedAt == nil {
		t.Errorf("resolve should have stamped last_used_at")
	}

	// wrong-neighbor and wrong-YEAR revokes must both be no-ops: the DELETE is
	// scoped to the exact Beleg the caller is acting on.
	if revoked, err := st.RevokeBelegShare(ctx, id, nid+999, yearID); err != nil || revoked {
		t.Fatalf("cross-neighbor revoke must be a no-op, got (%v,%v)", revoked, err)
	}
	otherYearID, err := st.CreateBillingYear(ctx, yr+1, baseID, "Share-Jahr B")
	if err != nil {
		t.Fatalf("other year: %v", err)
	}
	if revoked, err := st.RevokeBelegShare(ctx, id, nid, otherYearID); err != nil || revoked {
		t.Fatalf("cross-year revoke must be a no-op, got (%v,%v)", revoked, err)
	}
	if _, _, ok, _ := st.ResolveBelegShare(ctx, hash); !ok {
		t.Fatalf("share must survive cross-neighbor/cross-year revoke attempts")
	}
	// real revoke: works once, then idempotent-false, and resolve dies
	if revoked, err := st.RevokeBelegShare(ctx, id, nid, yearID); err != nil || !revoked {
		t.Fatalf("revoke: (%v,%v)", revoked, err)
	}
	if revoked, _ := st.RevokeBelegShare(ctx, id, nid, yearID); revoked {
		t.Errorf("second revoke must report false")
	}
	if _, _, ok, _ := st.ResolveBelegShare(ctx, hash); ok {
		t.Errorf("revoked link must not resolve")
	}

	// expired link never resolves and is not listed
	expHash := hash + "-exp"
	if _, err := st.CreateBelegShare(ctx, expHash, nid, yearID, time.Now().Add(-time.Hour), "admin"); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, _, ok, _ := st.ResolveBelegShare(ctx, expHash); ok {
		t.Errorf("expired link must not resolve")
	}
	if shares, _ := st.ListBelegShares(ctx, nid, yearID); len(shares) != 0 {
		t.Errorf("expired/revoked links must not be listed: %+v", shares)
	}

	// last_used_at is calendar-day granular, decided by the DATABASE: a stamp
	// from just before midnight (yesterday's date) must be refreshed on the
	// next access, a stamp from just after midnight (today) must not.
	dayHash := hash + "-day"
	dayID, err := st.CreateBelegShare(ctx, dayHash, nid, yearID, time.Now().Add(24*time.Hour), "admin")
	if err != nil {
		t.Fatalf("create day-case: %v", err)
	}
	// 23:59:59 yesterday — under a naive `time.Since > 24h` this would be
	// "recent" and skipped; by calendar date it is a NEW day → must restamp.
	if _, err := pool.ExecContext(ctx,
		`UPDATE beleg_shares SET last_used_at = CURRENT_DATE - interval '1 second' WHERE id = $1`, dayID); err != nil {
		t.Fatalf("seed yesterday-stamp: %v", err)
	}
	if _, _, ok, err := st.ResolveBelegShare(ctx, dayHash); err != nil || !ok {
		t.Fatalf("resolve day-case: (%v,%v)", ok, err)
	}
	var restamped bool
	if err := pool.QueryRowContext(ctx,
		`SELECT last_used_at::date = CURRENT_DATE FROM beleg_shares WHERE id = $1`, dayID).Scan(&restamped); err != nil || !restamped {
		t.Errorf("pre-midnight stamp must be refreshed on a new calendar day (restamped=%v, err=%v)", restamped, err)
	}
	// 00:00:01 today — same calendar day → the stamp must stay untouched.
	if _, err := pool.ExecContext(ctx,
		`UPDATE beleg_shares SET last_used_at = CURRENT_DATE + interval '1 second' WHERE id = $1`, dayID); err != nil {
		t.Fatalf("seed midnight-stamp: %v", err)
	}
	if _, _, ok, err := st.ResolveBelegShare(ctx, dayHash); err != nil || !ok {
		t.Fatalf("resolve day-case 2: (%v,%v)", ok, err)
	}
	var untouched bool
	if err := pool.QueryRowContext(ctx,
		`SELECT last_used_at = CURRENT_DATE + interval '1 second' FROM beleg_shares WHERE id = $1`, dayID).Scan(&untouched); err != nil || !untouched {
		t.Errorf("same-day stamp must not be rewritten (untouched=%v, err=%v)", untouched, err)
	}
}
