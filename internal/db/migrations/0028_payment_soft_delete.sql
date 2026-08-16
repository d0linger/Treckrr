-- Soft-delete for payments so an accidental delete can be undone. Payments (unlike
-- entries/ledger, which have Storno/un-Storno) had no reversible mechanism. A
-- deleted_at timestamp hides the row from every sum/list; a background purge hard-
-- deletes rows older than the grace window. Additive, default NULL (= active).
ALTER TABLE payments ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE INDEX idx_payments_deleted ON payments(deleted_at) WHERE deleted_at IS NOT NULL;
