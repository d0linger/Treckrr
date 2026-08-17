-- Partial index for the hot "active bookings in a year" path: nearly every
-- summary/recalc/dunning query filters `AND NOT voided`, so an index that already
-- excludes voided rows is smaller and lets those scans skip cancelled entries.
-- Additive and idempotent; safe to run on the live database at boot.
CREATE INDEX IF NOT EXISTS idx_entries_year_active ON entries(billing_year_id) WHERE NOT voided;
