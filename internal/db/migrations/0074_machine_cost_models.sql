-- Transparent assumptions for an optional self-cost proposal. The active
-- self_cost_per_h remains independent and changes only by explicit action.
ALTER TABLE machines
    ADD COLUMN acquisition_cost NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (acquisition_cost >= 0),
    ADD COLUMN residual_value NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (residual_value >= 0),
    ADD COLUMN useful_years INT NOT NULL DEFAULT 0 CHECK (useful_years BETWEEN 0 AND 100),
    ADD COLUMN annual_hours NUMERIC(12,2) NOT NULL DEFAULT 0 CHECK (annual_hours >= 0),
    ADD COLUMN fuel_cost_per_h NUMERIC(12,4) NOT NULL DEFAULT 0 CHECK (fuel_cost_per_h >= 0),
    ADD COLUMN annual_maintenance NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (annual_maintenance >= 0),
    ADD COLUMN annual_insurance NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (annual_insurance >= 0),
    ADD COLUMN annual_other_cost NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (annual_other_cost >= 0);
