-- Ratenplan (Ausbaukarte 43): agreed installments for a neighbor's year, so a
-- "pays 200 EUR per month" arrangement is visible instead of living in someone's
-- head. Deliberately lean: planned rows only — actual money keeps flowing through
-- payments, and the UI derives each installment's state by comparing the paid sum
-- against the cumulative plan. No new money paths, no state to keep in sync.
--
-- ON DELETE CASCADE like dunning_notices (0043): the plan is an arrangement
-- about the debt, not a business record — it goes with the year/neighbor.

CREATE TABLE payment_plans (
    id              BIGSERIAL PRIMARY KEY,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    due_on          DATE NOT NULL,
    amount          NUMERIC(12,2) NOT NULL CHECK (amount > 0),
    note            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_plans_year_neighbor ON payment_plans (billing_year_id, neighbor_id);
