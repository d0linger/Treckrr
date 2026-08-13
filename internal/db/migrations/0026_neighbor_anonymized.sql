-- DSGVO Art. 17 (right to erasure) for a neighbor who has bookings and therefore
-- cannot be hard-deleted (GoBD/§ 132 BAO retention). Anonymizing clears the live
-- master record's personal data while the frozen invoice snapshots — which froze
-- the recipient's name/address at issuance and are legally retained — stay intact.
-- Additive flag, default false, so existing rows are unaffected.
ALTER TABLE neighbors ADD COLUMN anonymized BOOLEAN NOT NULL DEFAULT FALSE;
