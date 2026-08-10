-- Festschreibung: freeze the full invoice content at issuance so an issued
-- invoice is immutable (BAO §131, GoBD Belegfunktion) instead of being
-- recomputed live from mutable bookings on every render. Also enables the
-- Storno / Gutschrift document history and a payable-invoice bank reference.
--
-- All columns are additive and nullable/defaulted, so existing rows and every
-- existing total are unaffected until the app backfills a snapshot for the
-- already-issued invoices (store.BackfillInvoiceSnapshots, run once on boot).

ALTER TABLE invoices
    ADD COLUMN net                  NUMERIC(14,2),          -- frozen Leistungsentgelt (net)
    ADD COLUMN vat_rate             NUMERIC(5,2),           -- frozen USt rate (%)
    ADD COLUMN vat_amount           NUMERIC(14,2),          -- frozen USt amount
    ADD COLUMN gross                NUMERIC(14,2),          -- frozen brutto
    ADD COLUMN show_vat             BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN tax_mode             TEXT NOT NULL DEFAULT '',   -- kleinunternehmer|pauschal|regel
    ADD COLUMN tax_note             TEXT NOT NULL DEFAULT '',
    ADD COLUMN service_from         DATE,                   -- Leistungszeitraum (§11)
    ADD COLUMN service_to           DATE,
    ADD COLUMN issuer               JSONB,                  -- {name,address,uid} frozen
    ADD COLUMN recipient            JSONB,                  -- {name,address,tax_id} frozen
    ADD COLUMN lines                JSONB,                  -- [{date,label,unit,qty,unit_price,cost}]
    ADD COLUMN content_hash         TEXT NOT NULL DEFAULT '',   -- sha256 of the canonical snapshot
    ADD COLUMN kind                 TEXT NOT NULL DEFAULT 'invoice',  -- invoice|storno|gutschrift
    ADD COLUMN status               TEXT NOT NULL DEFAULT 'issued',   -- issued|canceled
    ADD COLUMN references_invoice_id BIGINT REFERENCES invoices(id) ON DELETE SET NULL,
    ADD COLUMN payment_reference    TEXT NOT NULL DEFAULT '';   -- e.g. RF creditor reference

-- A neighbor+year now has a *history* of documents (invoice, its storno, a
-- re-issued invoice, credit notes). Replace the "exactly one invoice per
-- neighbor+year" rule with "at most one ACTIVE issued invoice per neighbor+year"
-- so storno + re-issue works while the invariant still holds.
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_billing_year_id_neighbor_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS invoices_active_per_neighbor_year
    ON invoices (billing_year_id, neighbor_id)
    WHERE kind = 'invoice' AND status = 'issued';

-- Issuer bank details for a payable invoice (optional; blank => no IBAN line).
ALTER TABLE company ADD COLUMN iban TEXT NOT NULL DEFAULT '';
