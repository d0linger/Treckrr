-- GUI-editable backup schedule (separate volume and S3 cadences + retention).
-- Single row; the app seeds it from the BACKUP_* env on first boot.
CREATE TABLE backup_settings (
    id                    INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    volume_interval_hours INT NOT NULL DEFAULT 24,
    volume_keep           INT NOT NULL DEFAULT 7,
    s3_interval_hours     INT NOT NULL DEFAULT 24,
    s3_keep               INT NOT NULL DEFAULT 0
);
