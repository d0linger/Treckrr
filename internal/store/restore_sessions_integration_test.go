//go:build integration

package store_test

import "testing"

func TestRestoreReconciliationRevokesResurrectedSessions(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	var userID int64
	if err := pool.QueryRowContext(ctx, `INSERT INTO users (username,password_hash) VALUES ('restored-user','test-only') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO sessions (token,user_id,expires_at) VALUES ('restored-token',$1,now()+interval '1 day')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO webauthn_ceremonies (id,session_data,expires_at) VALUES ('restored-ceremony','{}',now()+interval '5 minutes')`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileAfterRestore(ctx); err != nil {
		t.Fatal(err)
	}
	var sessions, ceremonies int
	if err := pool.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM webauthn_ceremonies)`).Scan(&sessions, &ceremonies); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || ceremonies != 0 {
		t.Fatalf("restored auth state survived: sessions=%d ceremonies=%d", sessions, ceremonies)
	}
}
