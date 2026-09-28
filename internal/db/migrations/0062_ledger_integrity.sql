-- 0062: ledger integrity follow-ups. Additive only: no table rewrite, no
-- backfill, no validation scan over existing rows.

-- Recurring rules remember why they are waiting (e.g. the neighbor's invoice is
-- issued), so a blocked rule is visible instead of silently starving. A
-- constant default is a catalog-only change on PostgreSQL 11+.
ALTER TABLE recurring_entries
    ADD COLUMN last_error    TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_error_at TIMESTAMPTZ;

-- Server-side idempotency for the payment and installment forms: every render
-- carries a fresh key, a resubmitted form finds its row instead of inserting a
-- second one. Existing rows keep NULL and do not participate.
ALTER TABLE payments ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX idx_payments_idempotency
    ON payments (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE payment_plans ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX idx_payment_plans_idempotency
    ON payment_plans (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Money is stored in whole cents. payments.amount and neighbor_ledger.amount
-- are NUMERIC(14,4) for historical reasons, so a typed 9,995 used to be kept
-- exactly and left half-cent balances behind.
--
-- Deliberately a trigger that only looks at the amount being WRITTEN, not a
-- CHECK constraint: a NOT VALID check skips the historical scan but is still
-- enforced on every later UPDATE of an old row — voiding, soft-deleting or
-- anonymizing a legacy posting with a sub-cent amount would then fail even
-- though its amount is not touched. The trigger fires on INSERT and on an
-- UPDATE that actually changes the amount; historical rows stay as they are.
CREATE OR REPLACE FUNCTION money_amount_cents_guard() RETURNS trigger AS $$
BEGIN
    IF NEW.amount IS NOT NULL AND NEW.amount <> round(NEW.amount, 2) THEN
        RAISE EXCEPTION '%.amount must be whole cents (got %)', TG_TABLE_NAME, NEW.amount
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_amount_cents_ins
    BEFORE INSERT ON payments
    FOR EACH ROW EXECUTE FUNCTION money_amount_cents_guard();
CREATE TRIGGER payments_amount_cents_upd
    BEFORE UPDATE OF amount ON payments
    FOR EACH ROW WHEN (NEW.amount IS DISTINCT FROM OLD.amount)
    EXECUTE FUNCTION money_amount_cents_guard();

CREATE TRIGGER neighbor_ledger_amount_cents_ins
    BEFORE INSERT ON neighbor_ledger
    FOR EACH ROW EXECUTE FUNCTION money_amount_cents_guard();
CREATE TRIGGER neighbor_ledger_amount_cents_upd
    BEFORE UPDATE OF amount ON neighbor_ledger
    FOR EACH ROW WHEN (NEW.amount IS DISTINCT FROM OLD.amount)
    EXECUTE FUNCTION money_amount_cents_guard();
