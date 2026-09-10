-- Ausbaukarte: Person mit dem Gespann buchen. One submit books the machine AND
-- the helper's Mannstunden; the Mannstunden entry points at the machine entry so
-- the pair stays visibly connected in every list.
--
-- ON DELETE SET NULL, not CASCADE: deleting the machine booking must not silently
-- delete the helper's recorded hours — the companion merely becomes standalone.
ALTER TABLE entries
    ADD COLUMN linked_entry_id BIGINT REFERENCES entries(id) ON DELETE SET NULL;

-- Partial: only companion rows carry a link, and the index exists so the FK's
-- ON DELETE SET NULL does not scan the whole table per deleted booking.
CREATE INDEX idx_entries_linked ON entries (linked_entry_id) WHERE linked_entry_id IS NOT NULL;
