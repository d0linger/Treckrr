-- Invoice settings (Ausbaukarte 48/49/55). Additive, defaults keep today's
-- behavior exactly.
--
-- invoice_prefix / invoice_start: the number format was hard-wired JAHR-NNN.
-- A prefix (alphanumeric, no separators — a dash inside the prefix would break
-- the split_part-based sequence scan) and a start number allow continuing an
-- existing external Nummernkreis. Defaults '' / 1 = unchanged numbering.
--
-- small_business_limit: the Kleinunternehmer revenue ceiling to warn against
-- (§ 6 Abs 1 Z 27 UStG). 0 = monitoring off (conservative default: no new
-- warnings appear unless configured).

ALTER TABLE company
    ADD COLUMN invoice_prefix       TEXT NOT NULL DEFAULT '' CHECK (invoice_prefix ~ '^[A-Za-z0-9]{0,10}$'),
    ADD COLUMN invoice_start        INT  NOT NULL DEFAULT 1  CHECK (invoice_start >= 1 AND invoice_start <= 999999),
    ADD COLUMN small_business_limit NUMERIC(12,2) NOT NULL DEFAULT 0 CHECK (small_business_limit >= 0);
