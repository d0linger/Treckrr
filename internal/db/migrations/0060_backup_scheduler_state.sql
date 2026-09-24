-- Cluster-wide backup scheduler progress. The singleton row prevents multiple
-- app replicas with separate status files from treating the same cron window as
-- due, and persists failure backoff across process restarts.
CREATE TABLE IF NOT EXISTS backup_scheduler_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    volume_last_success TIMESTAMPTZ,
    volume_retry_at TIMESTAMPTZ,
    s3_last_success TIMESTAMPTZ,
    s3_retry_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO backup_scheduler_state (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;
