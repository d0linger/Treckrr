-- Make the DATABASE the integrity boundary for financial and tax-relevant
-- history, instead of an application precheck.
--
-- 0038 and earlier reference neighbors/billing_years with ON DELETE CASCADE
-- throughout. The handler guard added alongside this migration counts the
-- dependent records before deleting, but a SELECT followed by a separate DELETE
-- is not atomic: a row inserted between the two is cascaded away regardless. The
-- background recurring-booking generator is a real concurrent writer, and a
-- manual DELETE in psql bypasses the guard entirely.
--
-- Only the tables that carry money or a tax document switch to RESTRICT:
--   entries, neighbor_ledger, payments, invoices, beleg_sends
-- These stay CASCADE on purpose — they hold no history of their own and deleting
-- the parent SHOULD remove them:
--   billing_year_neighbors (membership), beleg_shares (expiring links),
--   recurring_entries (templates)
-- Keeping billing_year_neighbors cascading is what still allows a neighbor who
-- was merely added to a year, and never booked against, to be removed.
--
-- This does not make records undeletable by design: the DSGVO erasure path is
-- AnonymizeNeighbor, an UPDATE that pseudonymizes in place — which is the correct
-- treatment for records under the § 132 BAO seven-year retention anyway.
--
-- Note for the test harness: fixture cleanup can no longer lean on the cascade
-- and must delete children explicitly (see fixtures_test.go).

ALTER TABLE entries
    DROP CONSTRAINT entries_neighbor_id_fkey,
    ADD CONSTRAINT entries_neighbor_id_fkey
        FOREIGN KEY (neighbor_id) REFERENCES neighbors(id) ON DELETE RESTRICT,
    DROP CONSTRAINT entries_billing_year_id_fkey,
    ADD CONSTRAINT entries_billing_year_id_fkey
        FOREIGN KEY (billing_year_id) REFERENCES billing_years(id) ON DELETE RESTRICT;

ALTER TABLE neighbor_ledger
    DROP CONSTRAINT neighbor_ledger_neighbor_id_fkey,
    ADD CONSTRAINT neighbor_ledger_neighbor_id_fkey
        FOREIGN KEY (neighbor_id) REFERENCES neighbors(id) ON DELETE RESTRICT,
    DROP CONSTRAINT neighbor_ledger_billing_year_id_fkey,
    ADD CONSTRAINT neighbor_ledger_billing_year_id_fkey
        FOREIGN KEY (billing_year_id) REFERENCES billing_years(id) ON DELETE RESTRICT;

ALTER TABLE payments
    DROP CONSTRAINT payments_neighbor_id_fkey,
    ADD CONSTRAINT payments_neighbor_id_fkey
        FOREIGN KEY (neighbor_id) REFERENCES neighbors(id) ON DELETE RESTRICT,
    DROP CONSTRAINT payments_billing_year_id_fkey,
    ADD CONSTRAINT payments_billing_year_id_fkey
        FOREIGN KEY (billing_year_id) REFERENCES billing_years(id) ON DELETE RESTRICT;

ALTER TABLE invoices
    DROP CONSTRAINT invoices_neighbor_id_fkey,
    ADD CONSTRAINT invoices_neighbor_id_fkey
        FOREIGN KEY (neighbor_id) REFERENCES neighbors(id) ON DELETE RESTRICT,
    DROP CONSTRAINT invoices_billing_year_id_fkey,
    ADD CONSTRAINT invoices_billing_year_id_fkey
        FOREIGN KEY (billing_year_id) REFERENCES billing_years(id) ON DELETE RESTRICT;

ALTER TABLE beleg_sends
    DROP CONSTRAINT beleg_sends_neighbor_id_fkey,
    ADD CONSTRAINT beleg_sends_neighbor_id_fkey
        FOREIGN KEY (neighbor_id) REFERENCES neighbors(id) ON DELETE RESTRICT,
    DROP CONSTRAINT beleg_sends_billing_year_id_fkey,
    ADD CONSTRAINT beleg_sends_billing_year_id_fkey
        FOREIGN KEY (billing_year_id) REFERENCES billing_years(id) ON DELETE RESTRICT;
