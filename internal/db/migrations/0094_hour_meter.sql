-- Hour-meter readings on tractor bookings. Additive.
--
-- Booked hours are typed, not measured. An own booking with a tractor can now
-- carry the start and end reading of the tractor's Betriebsstundenzähler as
-- evidence: the typed hours stay what is billed, and the data-quality page
-- checks the readings per tractor for overlaps, unbooked gaps and readings
-- that disagree with the hours. Both readings or none.
ALTER TABLE entries
    ADD COLUMN meter_start NUMERIC(10,2),
    ADD COLUMN meter_end NUMERIC(10,2),
    ADD CONSTRAINT entries_meter_pair CHECK (
        (meter_start IS NULL AND meter_end IS NULL)
        OR (meter_start IS NOT NULL AND meter_end IS NOT NULL AND meter_start >= 0 AND meter_end >= meter_start));

CREATE INDEX idx_entries_meter ON entries(tractor_id, entry_date) WHERE meter_end IS NOT NULL;
