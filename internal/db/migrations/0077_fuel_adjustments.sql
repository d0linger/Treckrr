-- Effective-dated operating-cost adjustments for catalog equipment prices.
-- Existing booking snapshots remain unchanged until the explicit recalculation
-- workflow is applied; new catalog bookings store the effective adjustment.
CREATE TABLE fuel_adjustments (
    id BIGSERIAL PRIMARY KEY,
    base_id BIGINT NOT NULL REFERENCES price_bases(id) ON DELETE CASCADE,
    effective_from DATE NOT NULL,
    label TEXT NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 100),
    amount_per_h NUMERIC(12,4) NOT NULL CHECK (amount_per_h >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (base_id, effective_from)
);

CREATE INDEX idx_fuel_adjustments_effective
    ON fuel_adjustments(base_id, effective_from DESC);

-- Reuse the pricing-freshness trigger introduced in 0040. This makes an
-- adjustment change visible to the existing preview/apply gate.
CREATE TRIGGER trg_fuel_adjustments_touch_base
    AFTER INSERT OR UPDATE OR DELETE ON fuel_adjustments
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_price_base();

-- Snapshot the applied adjustment on outgoing entries. Zero/empty defaults
-- preserve every historical row without a backfill or reinterpretation.
ALTER TABLE entries
    ADD COLUMN fuel_adjustment_label TEXT NOT NULL DEFAULT '',
    ADD COLUMN fuel_adjustment_per_h NUMERIC(12,4) NOT NULL DEFAULT 0
        CHECK (fuel_adjustment_per_h >= 0);
