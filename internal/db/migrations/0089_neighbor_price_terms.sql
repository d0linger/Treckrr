-- Standing price terms per neighbor, e.g. a family discount. Additive.
--
-- Skonto and payment terms could be set per neighbor, but a standing price
-- agreement had to be typed as a free rate on every booking. A term grants a
-- percentage off own catalog machine hours for a validity range. New and
-- edited bookings snapshot the effective term; existing open bookings change
-- only through the reviewed recalc. No term changes any current price.
CREATE TABLE neighbor_price_terms (
    id BIGSERIAL PRIMARY KEY,
    neighbor_id BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    label TEXT NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 100),
    discount_pct NUMERIC(5,2) NOT NULL CHECK (discount_pct > 0 AND discount_pct < 100),
    valid_from DATE NOT NULL,
    valid_to DATE CHECK (valid_to IS NULL OR valid_to >= valid_from),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_neighbor_price_terms_neighbor
    ON neighbor_price_terms(neighbor_id, valid_from DESC);

-- A changed term can reprice the neighbor's open bookings, so it moves the
-- revision of every basis the neighbor is billed under. The 0040 gate is an
-- upper bound; the exact recalc preview still decides what changed.
CREATE OR REPLACE FUNCTION treckrr_touch_neighbor_bases() RETURNS TRIGGER AS $$
BEGIN
    UPDATE price_bases
       SET items_updated_at = now()
     WHERE id IN (SELECT y.base_id
                    FROM billing_years y
                    JOIN billing_year_neighbors n ON n.billing_year_id = y.id
                   WHERE n.neighbor_id = COALESCE(NEW.neighbor_id, OLD.neighbor_id));
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_neighbor_price_terms_touch_bases
    AFTER INSERT OR UPDATE OR DELETE ON neighbor_price_terms
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_neighbor_bases();

-- Snapshot the applied term on outgoing entries: its label, the percentage and
-- the amount it took off the hourly rate. Zero/empty defaults keep every
-- historical row as it is.
ALTER TABLE entries
    ADD COLUMN price_term_label TEXT NOT NULL DEFAULT '',
    ADD COLUMN price_term_pct NUMERIC(5,2) NOT NULL DEFAULT 0
        CHECK (price_term_pct >= 0 AND price_term_pct < 100),
    ADD COLUMN price_term_per_h NUMERIC(12,4) NOT NULL DEFAULT 0
        CHECK (price_term_per_h >= 0);
