-- Recurring bookings: a rule holds a booking template (JSON) + a cadence, and the
-- maintenance loop materializes due occurrences as normal entries (idempotently,
-- via an idempotency key), into the neighbor's open billing year for that date.
-- Additive; pausing/deleting a rule never touches already-created bookings.
CREATE TABLE IF NOT EXISTS recurring_entries (
	id            BIGSERIAL PRIMARY KEY,
	neighbor_id   BIGINT      NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
	template      JSONB       NOT NULL,
	interval_kind TEXT        NOT NULL,          -- 'weekly' | 'monthly'
	next_run      DATE        NOT NULL,
	active        BOOLEAN     NOT NULL DEFAULT true,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_run_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_recurring_due ON recurring_entries(next_run) WHERE active;
