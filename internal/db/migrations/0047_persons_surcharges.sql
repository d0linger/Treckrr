-- Personenstamm, Mannstunden und Zuschläge (Ausbaukarte 56/57/58). Additive;
-- every default keeps today's behavior.
--
-- persons: helpers with their own hourly rate. The ÖKL Richtwerte state the
-- Fahrerlohn separately from the machine rate, and until now the only way to
-- bill it was a free unit with a hand-typed price on every single booking.
--
-- entries.person_id: which helper a Mannstunden booking belongs to. Nullable —
-- every existing booking and every machine booking has none. ON DELETE SET
-- NULL: removing a person from the master data must never take bookings with
-- it (they are billed history).
--
-- company.travel_flat / travel_per_km: Anfahrt as master data instead of a
-- retyped amount. Both 0 = the surcharge form stays hidden, nothing changes.

CREATE TABLE persons (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    hourly_rate NUMERIC(10,2) NOT NULL DEFAULT 0 CHECK (hourly_rate >= 0),
    note        TEXT NOT NULL DEFAULT '',
    archived    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE entries
    ADD COLUMN person_id BIGINT REFERENCES persons(id) ON DELETE SET NULL;

CREATE INDEX idx_entries_person ON entries (person_id) WHERE person_id IS NOT NULL;

ALTER TABLE company
    ADD COLUMN travel_flat   NUMERIC(10,2) NOT NULL DEFAULT 0 CHECK (travel_flat >= 0),
    ADD COLUMN travel_per_km NUMERIC(10,2) NOT NULL DEFAULT 0 CHECK (travel_per_km >= 0);
