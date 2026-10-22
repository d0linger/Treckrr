package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// auditSealBatch bounds one sealing transaction.
const auditSealBatch = 2000

// auditChains are the retention segments, one hash chain each.
var auditChains = []string{"short", "long"}

// auditChainOf assigns an action to its retention segment, the same split the
// staggered purge uses. The chain is stored at sealing, so the purge and the
// verification keep working if the action list changes later.
func auditChainOf(action string) string {
	if slices.Contains(shortLivedAuditActions, action) {
		return "short"
	}
	return "long"
}

// auditChainRow is the recorded content of one audit row.
type auditChainRow struct {
	ID                                             int64
	Username, Action, Entity, EntityID, Detail, IP string
	CreatedAt                                      time.Time
}

// auditRowHash is SHA-256 over the row's canonical, length-prefixed content,
// its chain position and its predecessor's hash. user_id is left out: it is a
// convenience link to the account, while the recorded username is the trail.
func auditRowHash(chain string, seq int64, prev []byte, r auditChainRow) []byte {
	h := sha256.New()
	for _, field := range []string{
		"treckrr-audit-v1", chain, strconv.FormatInt(seq, 10), hex.EncodeToString(prev),
		strconv.FormatInt(r.ID, 10), r.Username, r.Action, r.Entity, r.EntityID, r.Detail, r.IP,
		r.CreatedAt.UTC().Format(time.RFC3339Nano),
	} {
		fmt.Fprintf(h, "%d:%s;", len(field), field)
	}
	return h.Sum(nil)
}

// SealAuditLog links every committed, unsealed audit row into its chain, in
// batches, and returns how many it sealed. Rows of transactions still in flight
// are invisible here and are sealed by a later run.
func (s *Store) SealAuditLog(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.sealAuditBatch(ctx)
		total += n
		if err != nil || n < auditSealBatch || ctx.Err() != nil {
			return total, err
		}
	}
}

func (s *Store) sealAuditBatch(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_seal = 'on'`); err != nil {
		return 0, err
	}
	type head struct {
		seq  int64
		hash []byte
	}
	heads := map[string]*head{}
	rows, err := tx.QueryContext(ctx, `SELECT chain, last_seq, last_hash FROM audit_chain_heads ORDER BY chain FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var chain string
		h := &head{}
		if err := rows.Scan(&chain, &h.seq, &h.hash); err != nil {
			_ = rows.Close()
			return 0, err
		}
		heads[chain] = h
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, chain := range auditChains {
		if heads[chain] == nil {
			return 0, fmt.Errorf("audit chain head %q missing", chain)
		}
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT id, username, action, entity, entity_id, detail, ip, created_at
		  FROM audit_log WHERE row_hash IS NULL ORDER BY id LIMIT $1 FOR UPDATE`, auditSealBatch)
	if err != nil {
		return 0, err
	}
	var ids, seqs []int64
	var chains, prevs, hashes []string
	for rows.Next() {
		var r auditChainRow
		if err := rows.Scan(&r.ID, &r.Username, &r.Action, &r.Entity, &r.EntityID, &r.Detail, &r.IP, &r.CreatedAt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		chain := auditChainOf(r.Action)
		h := heads[chain]
		seq := h.seq + 1
		sum := auditRowHash(chain, seq, h.hash, r)
		ids, chains, seqs = append(ids, r.ID), append(chains, chain), append(seqs, seq)
		prevs, hashes = append(prevs, hex.EncodeToString(h.hash)), append(hashes, hex.EncodeToString(sum))
		h.seq, h.hash = seq, sum
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE audit_log a
		   SET chain = u.chain, chain_seq = u.seq, prev_hash = decode(u.prev, 'hex'), row_hash = decode(u.hash, 'hex')
		  FROM unnest($1::bigint[], $2::text[], $3::bigint[], $4::text[], $5::text[]) AS u(id, chain, seq, prev, hash)
		 WHERE a.id = u.id`, ids, chains, seqs, prevs, hashes); err != nil {
		return 0, err
	}
	for chain, h := range heads {
		if _, err := tx.ExecContext(ctx, `UPDATE audit_chain_heads SET last_seq=$2, last_hash=$3 WHERE chain=$1`,
			chain, h.seq, h.hash); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}

// AuditChainHead is one chain's sealed end, purge anchor and last check.
type AuditChainHead struct {
	Chain      string
	LastSeq    int64
	LastHash   []byte
	AnchorSeq  int64
	AnchorHash []byte
	VerifiedAt *time.Time
	VerifiedOK *bool
	VerifyNote string
}

// ShortHash renders the first bytes of the head hash for display.
func (h AuditChainHead) ShortHash() string {
	if len(h.LastHash) < 8 {
		return ""
	}
	return hex.EncodeToString(h.LastHash[:8])
}

// AuditChainHeads returns both chains' heads.
func (s *Store) AuditChainHeads(ctx context.Context) ([]AuditChainHead, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT chain, last_seq, last_hash, anchor_seq, anchor_hash, verified_at, verified_ok, verify_note
		  FROM audit_chain_heads ORDER BY chain DESC`)
	if err != nil {
		return nil, err
	}
	return scanAuditChainHeads(rows)
}

func scanAuditChainHeads(rows *sql.Rows) ([]AuditChainHead, error) {
	defer rows.Close()
	var out []AuditChainHead
	for rows.Next() {
		var h AuditChainHead
		var at sql.NullTime
		var ok sql.NullBool
		if err := rows.Scan(&h.Chain, &h.LastSeq, &h.LastHash, &h.AnchorSeq, &h.AnchorHash, &at, &ok, &h.VerifyNote); err != nil {
			return nil, err
		}
		if at.Valid {
			h.VerifiedAt = &at.Time
		}
		if ok.Valid {
			h.VerifiedOK = &ok.Bool
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AuditChainReport is the outcome of one verification.
type AuditChainReport struct {
	Checked  int
	Problems []string
}

// OK reports whether the verification found nothing.
func (r AuditChainReport) OK() bool { return len(r.Problems) == 0 }

// maxAuditChainProblems bounds the stored note; one problem already means alarm.
const maxAuditChainProblems = 10

// VerifyAuditChain recomputes every sealed row, checks each link and the
// chain ends, checks that the heads recorded from restored backups are still
// part of the live chain, and stores the result on the heads.
func (s *Store) VerifyAuditChain(ctx context.Context) (AuditChainReport, error) {
	var report AuditChainReport
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback() }()
	// Sealing and pruning take these locks in this order too. Keep the heads
	// and their rows stable until the verification result is recorded.
	headRows, err := tx.QueryContext(ctx, `
		SELECT chain, last_seq, last_hash, anchor_seq, anchor_hash, verified_at, verified_ok, verify_note
		  FROM audit_chain_heads ORDER BY chain FOR UPDATE`)
	if err != nil {
		return report, err
	}
	problem := func(format string, args ...any) {
		if len(report.Problems) < maxAuditChainProblems {
			report.Problems = append(report.Problems, fmt.Sprintf(format, args...))
		}
	}
	heads, err := scanAuditChainHeads(headRows)
	if err != nil {
		return report, err
	}
	for _, head := range heads {
		if err := verifyAuditChainRows(ctx, tx, head, &report.Checked, problem); err != nil {
			return report, err
		}
	}
	// Heads copied from restored backups: the row at that position must still
	// carry that hash, unless the retention purge has since removed it.
	rows, err := tx.QueryContext(ctx, `
		SELECT b.chain, b.seq, b.hash, b.source, a.row_hash, h.anchor_seq
		  FROM audit_chain_backup_heads b
		  JOIN audit_chain_heads h ON h.chain = b.chain
		  LEFT JOIN audit_log a ON a.chain = b.chain AND a.chain_seq = b.seq`)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var chain, source string
		var seq, anchor int64
		var want, got []byte
		if err := rows.Scan(&chain, &seq, &want, &source, &got, &anchor); err != nil {
			return report, err
		}
		if seq <= anchor {
			continue
		}
		if !bytes.Equal(want, got) {
			problem("Kette %s: Stand %d aus Sicherung %s fehlt oder weicht ab", chain, seq, source)
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	note := "Prüfkette vollständig (" + strconv.Itoa(report.Checked) + " Einträge)"
	if !report.OK() {
		note = report.Problems[0]
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE audit_chain_heads SET verified_at=now(), verified_ok=$1, verify_note=$2`, report.OK(), note); err != nil {
		return report, err
	}
	return report, tx.Commit()
}

func verifyAuditChainRows(ctx context.Context, tx *sql.Tx, head AuditChainHead, checked *int, problem func(string, ...any)) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, username, action, entity, entity_id, detail, ip, created_at, chain_seq, prev_hash, row_hash
		  FROM audit_log WHERE chain = $1 ORDER BY chain_seq`, head.Chain)
	if err != nil {
		return err
	}
	defer rows.Close()
	expectSeq, prev := head.AnchorSeq+1, head.AnchorHash
	for rows.Next() {
		var r auditChainRow
		var seq int64
		var rowPrev, rowHash []byte
		if err := rows.Scan(&r.ID, &r.Username, &r.Action, &r.Entity, &r.EntityID, &r.Detail, &r.IP, &r.CreatedAt,
			&seq, &rowPrev, &rowHash); err != nil {
			return err
		}
		*checked++
		if seq != expectSeq {
			problem("Kette %s: Eintrag %d fehlt (gefunden %d)", head.Chain, expectSeq, seq)
		}
		if !bytes.Equal(rowPrev, prev) {
			problem("Kette %s: Verkettung bei Eintrag %d (Protokoll-ID %d) unterbrochen", head.Chain, seq, r.ID)
		}
		if !bytes.Equal(auditRowHash(head.Chain, seq, rowPrev, r), rowHash) {
			problem("Kette %s: Inhalt von Eintrag %d (Protokoll-ID %d) wurde verändert", head.Chain, seq, r.ID)
		}
		expectSeq, prev = seq+1, rowHash
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if expectSeq-1 != head.LastSeq || !bytes.Equal(prev, head.LastHash) {
		problem("Kette %s: Ende bei %d passt nicht zum gespeicherten Stand %d", head.Chain, expectSeq-1, head.LastSeq)
	}
	return nil
}

// AuditChainVerifyDue reports whether a chain was never verified or its last
// verification is older than every.
func (s *Store) AuditChainVerifyDue(ctx context.Context, every time.Duration) (bool, error) {
	var due bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM audit_chain_heads WHERE verified_at IS NULL OR verified_at < now() - make_interval(secs => $1))`,
		every.Seconds()).Scan(&due)
	return due, err
}
