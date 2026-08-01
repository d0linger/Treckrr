-- Link the two sides of a carry-forward: the settle-out posting in the source
-- year and the opening posting in the target year share a transfer_id, so
-- deleting or voiding one side reverses BOTH atomically. Without this, removing
-- one side leaves the other, making the balance vanish from both years.
ALTER TABLE neighbor_ledger ADD COLUMN transfer_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_ledger_transfer ON neighbor_ledger(transfer_id) WHERE transfer_id <> '';
