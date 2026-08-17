-- Records when a neighbor's Beleg/invoice was sent or handed over, so the UI can
-- show "zuletzt versendet am …" and avoid double- or missed sends. Additive; a
-- history table (one row per send), the latest is queried per neighbor+year.
CREATE TABLE IF NOT EXISTS beleg_sends (
	id               BIGSERIAL PRIMARY KEY,
	billing_year_id  BIGINT      NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
	neighbor_id      BIGINT      NOT NULL REFERENCES neighbors(id)     ON DELETE CASCADE,
	sent_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
	channel          TEXT        NOT NULL DEFAULT 'manuell'
);
CREATE INDEX IF NOT EXISTS idx_beleg_sends_ny ON beleg_sends(neighbor_id, billing_year_id, sent_at DESC);
