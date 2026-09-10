-- Selbstkosten je Maschinen-Einsatzstunde (Ausbaukarte 83). Additive, default 0.
--
-- The app knows what a machine EARNS per hour (working width × € per AB·h, the
-- ÖKL-derived rate) but never what it COSTS to run. Without that there is no
-- Deckungsbeitrag, only turnover. 0 means "not configured": the contribution
-- margin is then simply not shown, so nothing changes for a farm that does not
-- track its own costs.
ALTER TABLE machines
    ADD COLUMN self_cost_per_h NUMERIC(10,2) NOT NULL DEFAULT 0 CHECK (self_cost_per_h >= 0);
