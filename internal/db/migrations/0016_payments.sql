-- Payments: dated amounts a neighbour paid toward a billing year, replacing the
-- single billing_year_neighbors.paid boolean. The payment side is decoupled from
-- year status — a completed year (bookings locked) still accepts payments until
-- the balance is settled, and partial payments are supported.
--   remaining = Σ(entries.cost, not voided) + Σ(ledger.amount, not voided) − Σ(payments.amount)
CREATE TABLE payments (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    amount          NUMERIC(14,4) NOT NULL,
    paid_on         DATE NOT NULL DEFAULT CURRENT_DATE,
    note            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_payments_year_neighbor ON payments(billing_year_id, neighbor_id);

-- Backfill: every (year, neighbour) currently marked paid becomes ONE payment
-- for that neighbour's full net balance (bookings + signed ledger), so existing
-- saldos are unchanged (net − payment = 0, i.e. still fully settled). The old
-- billing_year_neighbors.paid column is kept (now unused) rather than dropped, so
-- this migration is purely additive and reversible by redeploying the old binary.
INSERT INTO payments (billing_year_id, neighbor_id, amount, note)
SELECT byn.billing_year_id, byn.neighbor_id,
       COALESCE((SELECT SUM(e.cost) FROM entries e
                  WHERE e.neighbor_id = byn.neighbor_id
                    AND e.billing_year_id = byn.billing_year_id
                    AND NOT e.voided), 0)
     + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
                  WHERE l.neighbor_id = byn.neighbor_id
                    AND l.billing_year_id = byn.billing_year_id
                    AND NOT l.voided), 0),
       'Übernahme bestehender Zahlung'
FROM billing_year_neighbors byn
WHERE byn.paid = true;
