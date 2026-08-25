-- Cheap, exact-negative staleness gate for the "N Buchung(en) neu zu berechnen"
-- badge.
--
-- That badge was computed by running the FULL repricing simulation on every
-- dashboard and neighbor-detail render: ~6 queries plus a recompute of every
-- non-voided booking in the year, just to produce a count. The work is only ever
-- non-zero after someone edits the price basis, which is rare — the rest of the
-- time it is pure waste that grows with retained history.
--
-- The pricing rules live in Go (internal/calc), so the count itself cannot move
-- into SQL without duplicating business logic in two places. What CAN move is the
-- question "could anything be stale at all?":
--
--   price_bases.items_updated_at  bumped whenever a tractor, machine or load level
--                                 under that basis changes
--   entries.priced_at             when this booking's stored price was computed
--
-- If no booking was priced before the last basis change, none can differ, and the
-- expensive path is skipped entirely. When the gate does fire, the exact
-- simulation still decides — displayed numbers keep their current meaning.
--
-- The bump is a TRIGGER, not application code, so a write path that is added
-- later (or the base-copy routine) cannot forget it. Forgetting would make the
-- gate wrongly report "nothing stale" and HIDE a real repricing need, so this
-- must not depend on remembering.

ALTER TABLE price_bases ADD COLUMN items_updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE entries ADD COLUMN priced_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Existing bookings were priced when they were created.
UPDATE entries SET priced_at = created_at;

CREATE OR REPLACE FUNCTION treckrr_touch_price_base() RETURNS TRIGGER AS $$
BEGIN
    UPDATE price_bases
       SET items_updated_at = now()
     WHERE id = COALESCE(NEW.base_id, OLD.base_id);
    RETURN NULL; -- AFTER trigger; return value is ignored
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tractors_touch_base
    AFTER INSERT OR UPDATE OR DELETE ON tractors
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_price_base();

CREATE TRIGGER trg_machines_touch_base
    AFTER INSERT OR UPDATE OR DELETE ON machines
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_price_base();

CREATE TRIGGER trg_load_levels_touch_base
    AFTER INSERT OR UPDATE OR DELETE ON load_levels
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_price_base();

-- Supports the gate query (one index serves both the year and the year+neighbor form).
CREATE INDEX idx_entries_priced_at ON entries(billing_year_id, neighbor_id, priced_at)
    WHERE NOT voided;
