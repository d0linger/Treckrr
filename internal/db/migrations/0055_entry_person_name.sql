-- Keep booked/free-text helper names independently of the live person register.
-- Existing pricing, attribution, links and invoice snapshots are not rewritten.
ALTER TABLE entries ADD COLUMN person_name TEXT NOT NULL DEFAULT '';
