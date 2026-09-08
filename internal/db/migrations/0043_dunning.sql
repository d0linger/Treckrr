-- Dunning becomes stateful (Ausbaukarte 32-36). Additive only.
--
-- The stage used to be a URL parameter at print time: nobody could answer "which
-- Mahnung did Gruber get, and when?" — the one question a dunning ladder exists
-- for. dunning_notices records every sent/marked notice; the list shows the last
-- one per neighbor and the letters can reference it.
--
-- company gains per-stage fees and the Nachfrist length. Fees default to 0, so
-- nothing changes on existing letters until the operator sets them. Verzugszinsen
-- are deliberately NOT modelled here: the statutory rate is a moving legal
-- target (base rate + spread, B2B vs B2C) and a wrong default on a tax-adjacent
-- document is worse than none — flat fees cover the practical need.
--
-- neighbors gains an optional payment-term override (NULL = company default),
-- because individual terms between neighbors are common.

CREATE TABLE dunning_notices (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    invoice_number  TEXT NOT NULL,
    stage           INT NOT NULL CHECK (stage BETWEEN 0 AND 2),
    channel         TEXT NOT NULL DEFAULT 'manuell',   -- 'e-mail' | 'manuell'
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    grace_until     DATE,                              -- Nachfrist printed on the letter
    fee             NUMERIC(10,2) NOT NULL DEFAULT 0   -- Mahnspesen charged with this notice
);

CREATE INDEX idx_dunning_notices_ny ON dunning_notices (billing_year_id, neighbor_id, sent_at DESC);

ALTER TABLE company
    ADD COLUMN dunning_fee_1      NUMERIC(10,2) NOT NULL DEFAULT 0,
    ADD COLUMN dunning_fee_2      NUMERIC(10,2) NOT NULL DEFAULT 0,
    ADD COLUMN dunning_grace_days INT NOT NULL DEFAULT 14 CHECK (dunning_grace_days BETWEEN 0 AND 365);

ALTER TABLE neighbors
    ADD COLUMN payment_term_days INT CHECK (payment_term_days BETWEEN 0 AND 365);
