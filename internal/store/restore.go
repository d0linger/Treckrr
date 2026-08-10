package store

import (
	"context"

	"treckrr/internal/db"
)

// ReconcileAfterRestore brings the database back in step with THIS binary right
// after an in-place restore (GUI upload / S3), so no container restart is needed.
// A restored backup can be a schema version behind the running code — its
// schema_migrations, and thus the columns its migrations added, are the backup's —
// and `pg_restore --clean` dropped and recreated every table, leaving pooled
// connections with cached statements bound to the old relations. Three steps, in
// order and mirroring startup:
//
//  1. Reset the pool — discard connections holding stale cached plans.
//  2. Migrate — re-apply whatever the backup lacked (forward-only; a backup NEWER
//     than this binary is left as-is, there is no matching migration to apply).
//  3. Backfill invoice snapshots — the same idempotent step run at boot, so a
//     restored pre-Festschreibung backup gets its frozen snapshots without a
//     restart.
func (s *Store) ReconcileAfterRestore(ctx context.Context) error {
	db.ResetPool(s.db)
	if err := db.Migrate(ctx, s.db); err != nil {
		return err
	}
	_, err := s.BackfillInvoiceSnapshots(ctx)
	return err
}
