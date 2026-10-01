-- Append-only delivery-attempt evidence for the operational mail center.
-- Existing outbox rows remain untouched; their aggregate attempt counter stays
-- visible even when no per-attempt rows exist from older application versions.
ALTER TABLE mail_outbox
    ADD COLUMN attempt_seq INT NOT NULL DEFAULT 0;

CREATE TABLE mail_outbox_attempts (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    outbox_id       BIGINT NOT NULL REFERENCES mail_outbox(id) ON DELETE CASCADE,
    attempt_no      INT NOT NULL CHECK (attempt_no > 0),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    data_started_at TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    outcome         TEXT NOT NULL DEFAULT 'sending'
                    CHECK (outcome IN ('sending','sent','retry','failed','ambiguous')),
    detail          TEXT NOT NULL DEFAULT '',
    UNIQUE (outbox_id, attempt_no)
);

CREATE INDEX idx_mail_outbox_attempts_outbox
    ON mail_outbox_attempts (outbox_id, attempt_no DESC);
