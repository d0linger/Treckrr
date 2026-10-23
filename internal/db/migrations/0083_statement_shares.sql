-- Running statement links ("Zwischenstand teilen"). Additive only.
--
-- An invoice link (beleg_shares) is bound to one frozen document. Before any
-- invoice exists, a neighbor can only check the year's work by asking. These
-- links show the work not yet invoiced plus the annual account, read-only, so
-- objections arrive while a correction is still an edit and not a Storno.
--
-- Like beleg_shares only the SHA-256 of the bearer token is stored, revocation
-- is a hard DELETE and expired rows are pruned on the next create. Feedback
-- freezes the label and amount of the line it referred to, because the open
-- lines keep changing until the invoice is issued.
CREATE TABLE statement_shares (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash      TEXT NOT NULL UNIQUE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ
);
CREATE INDEX idx_statement_shares_account ON statement_shares (neighbor_id, billing_year_id);

CREATE TABLE statement_feedback (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    share_id        BIGINT REFERENCES statement_shares(id) ON DELETE SET NULL,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('confirmed', 'disputed')),
    line_label      TEXT NOT NULL DEFAULT '' CHECK (char_length(line_label) <= 300),
    line_amount     NUMERIC(14,2),
    message         TEXT NOT NULL DEFAULT '' CHECK (char_length(message) <= 2000),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_statement_feedback_account ON statement_feedback (neighbor_id, billing_year_id, created_at DESC);
CREATE INDEX idx_statement_feedback_share ON statement_feedback (share_id, created_at);

-- Objections from either link type become a notification for writers.
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
    CHECK (kind IN ('due', 'recurring', 'import', 'mail', 'backup', 'feedback'));
