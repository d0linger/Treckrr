-- A scheduled backup records that it started before doing any heavy work. If
-- the process dies mid-run (OOM kill, panic), the marker survives and the next
-- scheduler tick treats the run as failed instead of starting the same heavy
-- job again right after every restart. Nullable, no default: metadata-only.
ALTER TABLE backup_scheduler_state
    ADD COLUMN IF NOT EXISTS volume_attempt_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS s3_attempt_at TIMESTAMPTZ;

-- Validate the widened outbox status check from 0063 in its own transaction.
-- It is a strict superset of the previous constraint, which every existing row
-- already satisfied, so this cannot fail on existing data.
ALTER TABLE mail_outbox
    VALIDATE CONSTRAINT mail_outbox_status_chk;
