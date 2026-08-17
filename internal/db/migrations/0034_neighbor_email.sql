-- Optional e-mail address per neighbor, so a Beleg/Rechnung can be sent to them.
-- Additive; empty by default.
ALTER TABLE neighbors ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
