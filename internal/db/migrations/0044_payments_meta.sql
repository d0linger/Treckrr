-- Payments grow up (Ausbaukarte 38-42), plus the neighbor IBAN the bank-import
-- matcher will use (45). Additive only.
--
-- method: how the money arrived (Überweisung, bar, Verrechnung …). Under
-- neighbors, cash and in-kind settlement are the NORMAL case, and until now the
-- only place to record that was the free-text note.
--
-- invoice_id: which frozen invoice a payment settles. Payments used to hang off
-- (year, neighbor) only, so after a storno + re-issue the attribution was
-- guesswork. Linked forward-only — existing rows stay NULL, because inventing
-- the mapping in a migration would be exactly the guesswork the column removes.
-- ON DELETE SET NULL: hard-deleting an invoice (which the app never does to an
-- issued one) must not take the payment with it.
--
-- company.skonto_pct/skonto_days: the Skonto OFFER printed on the invoice
-- ("2 % bei Zahlung binnen 14 Tagen"). Both default 0 = no clause, nothing
-- changes. The § 16 credit at payment time already existed; what was missing is
-- the promise the credit refers to.

ALTER TABLE payments
    ADD COLUMN method     TEXT NOT NULL DEFAULT '',
    ADD COLUMN invoice_id BIGINT REFERENCES invoices(id) ON DELETE SET NULL;

CREATE INDEX idx_payments_invoice ON payments (invoice_id) WHERE invoice_id IS NOT NULL;

ALTER TABLE company
    ADD COLUMN skonto_pct  NUMERIC(4,1) NOT NULL DEFAULT 0 CHECK (skonto_pct >= 0 AND skonto_pct <= 10),
    ADD COLUMN skonto_days INT NOT NULL DEFAULT 0 CHECK (skonto_days >= 0 AND skonto_days <= 90);

ALTER TABLE neighbors
    ADD COLUMN iban TEXT NOT NULL DEFAULT '';
