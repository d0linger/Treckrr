-- Switch the backup schedule from fixed hour-intervals to standard 5-field cron
-- (Minute Stunde Tag Monat Wochentag). Empty = that destination is off.
ALTER TABLE backup_settings ADD COLUMN volume_cron TEXT NOT NULL DEFAULT '0 3 * * *';
ALTER TABLE backup_settings ADD COLUMN s3_cron     TEXT NOT NULL DEFAULT '0 4 * * *';
ALTER TABLE backup_settings DROP COLUMN volume_interval_hours;
ALTER TABLE backup_settings DROP COLUMN s3_interval_hours;
