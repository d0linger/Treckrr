-- Bring ledger postings to feature parity with bookings: an explicit (editable)
-- posting date, plus a traceable void (Storno) that keeps the row but excludes
-- it from the balance. Existing rows default to their creation day, not voided.
ALTER TABLE neighbor_ledger ADD COLUMN posting_date DATE NOT NULL DEFAULT CURRENT_DATE;
ALTER TABLE neighbor_ledger ADD COLUMN voided       BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE neighbor_ledger ADD COLUMN void_reason  TEXT NOT NULL DEFAULT '';
