-- Invoice support (Feature 4). Company (Absender) details as a single-row table,
-- a per-neighbour address for the recipient block, and an invoices table that
-- fixes a sequential number per year when a Beleg is issued as a Rechnung.
CREATE TABLE company (
    id       INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    name     TEXT NOT NULL DEFAULT '',
    address  TEXT NOT NULL DEFAULT '',
    tax_id   TEXT NOT NULL DEFAULT '',
    -- AT default: flat-rate agriculture (pauschaliert), no separate VAT shown.
    tax_note TEXT NOT NULL DEFAULT 'Umsätze eines pauschalierten land- und forstwirtschaftlichen Betriebs gemäß § 22 UStG; keine gesonderte Umsatzsteuer.',
    tax_mode TEXT NOT NULL DEFAULT 'pauschal', -- 'pauschal' | 'regel'
    vat_rate NUMERIC(5,2) NOT NULL DEFAULT 0     -- % shown only in 'regel' mode
);
INSERT INTO company (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

ALTER TABLE neighbors ADD COLUMN address TEXT NOT NULL DEFAULT '';

-- One issued invoice per (year, neighbour); number is sequential within a year
-- and fixed at issue time so the document stays stable.
CREATE TABLE invoices (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    number          TEXT NOT NULL,
    issued_on       DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (billing_year_id, neighbor_id)
);
