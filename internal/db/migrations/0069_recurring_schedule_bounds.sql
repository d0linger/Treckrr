-- Optional series end date and an append-only record of deliberately skipped
-- occurrences. Existing rules keep NULL (unbounded) and behave exactly as before.
ALTER TABLE recurring_entries
    ADD COLUMN ends_on DATE;

ALTER TABLE recurring_entries
    ADD CONSTRAINT recurring_entries_schedule_bounds_chk
    CHECK (ends_on IS NULL OR NOT active OR ends_on >= next_run) NOT VALID;

CREATE TABLE recurring_exceptions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    recurring_id BIGINT NOT NULL REFERENCES recurring_entries(id) ON DELETE CASCADE,
    skipped_on   DATE NOT NULL,
    reason       TEXT NOT NULL DEFAULT 'operator_skip',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (recurring_id, skipped_on)
);

CREATE INDEX idx_recurring_exceptions_rule
    ON recurring_exceptions (recurring_id, skipped_on DESC);
