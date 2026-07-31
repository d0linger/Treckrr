-- Quantity/unit billing: each booking gets a unit, a quantity and a unit price.
--   cost = quantity * unit_price
-- Hour-based bookings keep working as the special case unit = 'h', where
-- quantity = hours and unit_price = hourly_rate. Existing rows are backfilled
-- accordingly, so nothing changes for them and every saldo stays identical.
ALTER TABLE entries ADD COLUMN unit TEXT NOT NULL DEFAULT 'h';
ALTER TABLE entries ADD COLUMN quantity NUMERIC(14,4) NOT NULL DEFAULT 0;
ALTER TABLE entries ADD COLUMN unit_price NUMERIC(14,4) NOT NULL DEFAULT 0;
UPDATE entries SET quantity = hours, unit_price = hourly_rate WHERE unit = 'h';
