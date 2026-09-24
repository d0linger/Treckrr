-- Durable delivery identity and claim state for outbound mail. Existing rows
-- remain valid and keep their NULL identity; all new application-created rows
-- carry a delivery_key and stable RFC Message-ID.
ALTER TABLE mail_outbox
    ADD COLUMN delivery_key TEXT,
    ADD COLUMN message_id TEXT,
    ADD COLUMN claimed_at TIMESTAMPTZ,
    ADD COLUMN terminal_at TIMESTAMPTZ,
    ADD COLUMN redacted_at TIMESTAMPTZ;

CREATE UNIQUE INDEX idx_mail_outbox_delivery_key
    ON mail_outbox (delivery_key)
    WHERE delivery_key IS NOT NULL;

-- Expand the state machine without touching existing row values. "sending" is
-- a claim held by exactly one worker; "ambiguous" is terminal and never retried.
ALTER TABLE mail_outbox
    DROP CONSTRAINT mail_outbox_status_chk,
    ADD CONSTRAINT mail_outbox_status_chk
    CHECK (status IN ('pending','sending','sent','failed','ambiguous'));

CREATE INDEX idx_mail_outbox_stale_claim
    ON mail_outbox (claimed_at)
    WHERE status = 'sending';
