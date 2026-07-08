-- Bidirectional per-neighbor/year ledger: manual amounts that net against the
-- work bookings so a settlement can go either way.
--   amount > 0  = additional receivable (the neighbour owes more)
--   amount < 0  = payable (I owe the neighbour)
-- The year balance for a neighbour is  Σ(entries.cost, not voided) + Σ(amount).
CREATE TABLE neighbor_ledger (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    amount          NUMERIC(14,4) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ledger_year_neighbor ON neighbor_ledger(billing_year_id, neighbor_id);
