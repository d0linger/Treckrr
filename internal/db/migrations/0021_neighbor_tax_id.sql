-- Recipient UID/tax number for the invoice "An" block. Required by § 11 UStG on
-- invoices over EUR 10,000 gross; optional otherwise. Additive, default empty.
ALTER TABLE neighbors ADD COLUMN tax_id TEXT NOT NULL DEFAULT '';
