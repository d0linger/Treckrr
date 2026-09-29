-- Freeze every machine component used by a booking. Existing bookings are not
-- backfilled: their historical component rates cannot be reconstructed safely
-- and remain identifiable as legacy estimates in reports.
CREATE TABLE entry_machine_snapshots (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id        BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    machine_id      BIGINT REFERENCES machines(id) ON DELETE SET NULL,
    machine_label   TEXT NOT NULL,
    hourly_rate     NUMERIC(14,4) NOT NULL CHECK (hourly_rate >= 0),
    self_cost_per_h NUMERIC(14,4) NOT NULL CHECK (self_cost_per_h >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_entry_machine_snapshots_source
    ON entry_machine_snapshots (entry_id, machine_id)
    WHERE machine_id IS NOT NULL;
CREATE INDEX idx_entry_machine_snapshots_entry
    ON entry_machine_snapshots (entry_id);
