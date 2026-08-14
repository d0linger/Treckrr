package store

import (
	"context"

	"github.com/d0linger/treckrr/internal/models"
)

// EnsureBackupSettings inserts the single settings row from the given defaults if
// it does not exist yet. Idempotent.
func (s *Store) EnsureBackupSettings(ctx context.Context, def models.BackupSettings) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_settings (id, volume_cron, volume_keep, s3_cron, s3_keep)
		 VALUES (1,$1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`,
		def.VolumeCron, def.VolumeKeep, def.S3Cron, def.S3Keep)
	return err
}

// GetBackupSettings returns the current backup schedule.
func (s *Store) GetBackupSettings(ctx context.Context) (models.BackupSettings, error) {
	var b models.BackupSettings
	err := s.db.QueryRowContext(ctx,
		`SELECT volume_cron, volume_keep, s3_cron, s3_keep
		   FROM backup_settings WHERE id=1`).
		Scan(&b.VolumeCron, &b.VolumeKeep, &b.S3Cron, &b.S3Keep)
	return b, err
}

// UpdateBackupSettings persists an edited backup schedule.
func (s *Store) UpdateBackupSettings(ctx context.Context, b models.BackupSettings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_settings
		    SET volume_cron=$1, volume_keep=$2, s3_cron=$3, s3_keep=$4
		  WHERE id=1`,
		b.VolumeCron, b.VolumeKeep, b.S3Cron, b.S3Keep)
	return err
}
