-- Indexes the schema always implied but never had, plus two guard rails on
-- backup_settings. Strictly additive: no data is touched. (Ausbaukarte 11-13.)
--
-- entries carries three ON DELETE SET NULL references (gespann, tractor, load
-- level) with NO index on the referencing column, so deleting a price-basis row
-- sequential-scans the biggest table once per deleted row. The same pattern
-- repeats across the schema:
--   * base_id on the master-data tables is only covered as the leading column
--     of a UNIQUE (base_id, name) where one exists at all — billing_years has
--     nothing, so the RESTRICT check on price_bases deletion scans it;
--   * audit_log.user_id backs an ON DELETE SET NULL and the per-user filter of
--     the fastest-growing table;
--   * payments/neighbor_ledger only index (billing_year_id, neighbor_id), so
--     the neighbor-side RESTRICT checks added in 0039 scan both on a neighbor
--     delete;
--   * the join tables' composite PKs are useless in the machine->rig /
--     neighbor->year direction;
--   * recurring_entries has only the partial due-index, nothing for the
--     neighbor cascade.
--
-- On today's data sizes every one of these builds in milliseconds; they are for
-- the years of history this app is meant to accumulate.

CREATE INDEX IF NOT EXISTS idx_entries_gespann ON entries (gespann_id) WHERE gespann_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_entries_tractor ON entries (tractor_id) WHERE tractor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_entries_load ON entries (load_level_id) WHERE load_level_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_billing_years_base ON billing_years (base_id);
CREATE INDEX IF NOT EXISTS idx_gespanne_tractor ON gespanne (tractor_id) WHERE tractor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_gespanne_load ON gespanne (load_level_id) WHERE load_level_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_log (user_id) WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_payments_neighbor ON payments (neighbor_id);
CREATE INDEX IF NOT EXISTS idx_ledger_neighbor ON neighbor_ledger (neighbor_id);

CREATE INDEX IF NOT EXISTS idx_gespann_machines_machine ON gespann_machines (machine_id);
CREATE INDEX IF NOT EXISTS idx_entry_machines_machine ON entry_machines (machine_id);
CREATE INDEX IF NOT EXISTS idx_byn_neighbor ON billing_year_neighbors (neighbor_id);

CREATE INDEX IF NOT EXISTS idx_recurring_neighbor ON recurring_entries (neighbor_id);

-- Retention guard rails: the handler validates these, but a direct SQL edit
-- could silently disable rotation (volume_keep 0 means "keep nothing checked").
-- Existing rows satisfy both (defaults 7 and 0).
ALTER TABLE backup_settings ADD CONSTRAINT backup_settings_volume_keep_min CHECK (volume_keep >= 1);
ALTER TABLE backup_settings ADD CONSTRAINT backup_settings_s3_keep_min CHECK (s3_keep >= 0);
