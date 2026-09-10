-- Durable retry queue for outbound mail (Ausbaukarte 19/20).
--
-- mail.Send runs synchronously in the request so the user gets an immediate
-- answer — that stays. What was lost before: a FAILED send evaporated with the
-- flash message. One SMTP hiccup during the yearly invoice run meant re-clicking
-- every affected neighbor by hand, provided anyone still remembered which ones.
--
-- A failed send is now parked here with its rendered attachment and retried by
-- the maintenance loop with a growing backoff. The rendered bytes are stored
-- rather than re-derived at send time: a Mahnung depends on the dunning stage
-- chosen at click time, and an invoice could have been stornoed since — the
-- message that failed is the message that should eventually arrive.
--
-- Additive only; nothing existing is touched.

CREATE TABLE mail_outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind            TEXT NOT NULL,               -- 'beleg' | 'mahnung'
    neighbor_id     BIGINT REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT REFERENCES billing_years(id) ON DELETE CASCADE,
    recipient       TEXT NOT NULL,
    subject         TEXT NOT NULL,
    body            TEXT NOT NULL,
    att_name        TEXT NOT NULL DEFAULT '',
    att_type        TEXT NOT NULL DEFAULT '',
    att_data        BYTEA,
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending | sent | failed
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT NOT NULL DEFAULT '',
    sent_at         TIMESTAMPTZ,
    CONSTRAINT mail_outbox_status_chk CHECK (status IN ('pending','sent','failed'))
);

-- The worker's polling query: only due, still-pending rows.
CREATE INDEX idx_mail_outbox_due ON mail_outbox (next_attempt_at) WHERE status = 'pending';
