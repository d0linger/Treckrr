-- mail_outbox.meta: the retry loop must be able to finish a Mahnung's
-- bookkeeping on delivery (dunning_notices + beleg_sends). The synchronous
-- path has the mahnungView in hand; the outbox only had recipient/subject/
-- body, so a reminder delivered on retry left no trace in the Mahnhistorie —
-- and the operator ran the same stage again against people who already got it.
-- The stage/fee/grace/invoice number now travel with the parked mail.
ALTER TABLE mail_outbox
    ADD COLUMN meta JSONB NOT NULL DEFAULT '{}';

-- audit_log.username: the /protokoll filter dropdown does SELECT DISTINCT
-- username over the whole table on every render. With §132 BAO retention (up
-- to 7 years) that is a full heap scan for a handful of names; the index turns
-- it into an index-only scan.
CREATE INDEX idx_audit_username ON audit_log (username);
