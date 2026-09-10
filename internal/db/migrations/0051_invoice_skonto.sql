-- Skonto terms belong to the § 11 snapshot: the clause is part of the invoice's
-- payment terms, so it must render identically on the Beleg, the PDF and the
-- share link, and never change after Festschreibung. Until now it was computed
-- live from the company row at render time — the PDF omitted it entirely, and
-- the on-screen clause silently vanished once the (timezone-skewed) deadline
-- passed. NULL for every document issued before this migration and for the
-- storno/gutschrift kinds: those simply carry no clause.
ALTER TABLE invoices
    ADD COLUMN skonto_pct   NUMERIC(4,1),
    ADD COLUMN skonto_until DATE;
