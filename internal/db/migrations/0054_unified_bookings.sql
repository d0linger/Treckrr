-- Optional service details for counterclaims and free positions. Existing
-- ledger amounts, descriptions, transfer links and invoice snapshots stay as-is.
ALTER TABLE neighbor_ledger
    ADD COLUMN booking JSONB,
    ADD COLUMN idempotency_key TEXT;

-- Only new keyed submissions participate; legacy and carry-forward rows remain
-- nullable. Account-scoped locking in the store also checks entry replay keys.
CREATE UNIQUE INDEX idx_neighbor_ledger_idempotency
    ON neighbor_ledger (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Preserve canonical request identity for new unified-form offline retries.
-- Historical entries retain their original replay behavior; no backfill needed.
ALTER TABLE entries ADD COLUMN request_fingerprint TEXT;
