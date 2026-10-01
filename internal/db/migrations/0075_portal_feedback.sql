-- Audit public portal use without ever storing bearer tokens or request data.
CREATE TABLE beleg_share_events (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    share_id        BIGINT REFERENCES beleg_shares(id) ON DELETE SET NULL,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    event           TEXT NOT NULL CHECK (event IN ('access', 'download', 'confirm', 'dispute')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_beleg_share_events_account ON beleg_share_events (neighbor_id, billing_year_id, created_at DESC);

-- A response is bound to the immutable invoice content hash. It is
-- communication only and can never edit bookings or invoice values.
CREATE TABLE beleg_feedback (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    share_id        BIGINT REFERENCES beleg_shares(id) ON DELETE SET NULL,
    invoice_id      BIGINT NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    content_hash    TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('confirmed', 'disputed')),
    line_position   INT CHECK (line_position IS NULL OR line_position > 0),
    message         TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_beleg_feedback_account ON beleg_feedback (neighbor_id, billing_year_id, created_at DESC);
