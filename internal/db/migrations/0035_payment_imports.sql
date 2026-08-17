-- De-duplication ledger for bank-statement imports: one row per already-imported
-- transaction (hash of date+amount+reference+name), so re-importing a statement
-- never double-records a payment. Additive.
CREATE TABLE IF NOT EXISTS payment_imports (
	hash        TEXT PRIMARY KEY,
	imported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
