-- Field reference on bookings. Additive.
--
-- Hectares are a supported unit, but a booking could not say which field,
-- so "what did the Hinterfeld cost this year?" had no answer. A neighbor can
-- keep a field list (name, optional area and Schlagnummer); an own booking
-- names a listed field or a free-text one. The booking snapshots the name in
-- field_label, so renaming or archiving a field never rewrites history. No
-- maps, no GPS.
CREATE TABLE neighbor_fields (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    neighbor_id BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    area_ha NUMERIC(10,4) CHECK (area_ha IS NULL OR area_ha > 0),
    plot_number TEXT NOT NULL DEFAULT '' CHECK (length(plot_number) <= 40),
    archived BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_neighbor_fields_active_name
    ON neighbor_fields(neighbor_id, lower(btrim(name))) WHERE NOT archived;

ALTER TABLE entries
    ADD COLUMN field_id BIGINT REFERENCES neighbor_fields(id) ON DELETE SET NULL,
    ADD COLUMN field_label TEXT NOT NULL DEFAULT '' CHECK (length(field_label) <= 100);

CREATE INDEX idx_entries_field ON entries(field_id) WHERE field_id IS NOT NULL;
