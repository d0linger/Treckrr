-- Restore safety and crash-safe delivery claims for outbound mail. Additive
-- only: every existing row keeps its status and values.
--
-- "held" parks rows that a backup restore brought back as pending/sending. A
-- restored backup cannot know whether such a mail was delivered after the
-- backup was taken, so it waits for an operator to release or discard it.
-- held_at records when the row was parked.
--
-- delivery_phase tracks how far a claim got: 'claimed' (not yet at SMTP DATA)
-- or 'data' (DATA started). Only a claim this release marked 'claimed' may be
-- returned to the queue after a crash; NULL (claims made by older binaries)
-- keeps the conservative "ambiguous" treatment.
ALTER TABLE mail_outbox
    ADD COLUMN held_at TIMESTAMPTZ,
    ADD COLUMN delivery_phase TEXT;

-- Widen the state machine. The new set is a superset of the old one, so every
-- existing row already satisfies it; NOT VALID skips the full-table scan under
-- this transaction's DDL lock and still checks every new write immediately.
-- Migration 0065 validates historical rows after this lock is released.
ALTER TABLE mail_outbox
    DROP CONSTRAINT mail_outbox_status_chk,
    ADD CONSTRAINT mail_outbox_status_chk
    CHECK (status IN ('pending','sending','sent','failed','ambiguous','held')) NOT VALID;
