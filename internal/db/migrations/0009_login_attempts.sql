-- Persistent, restart- and instance-safe rate-limiting state for login and
-- other sensitive actions (replaces the previous in-memory limiter).
CREATE TABLE login_attempts (
    key          TEXT PRIMARY KEY,
    fails        INT NOT NULL DEFAULT 0,
    window_start TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_login_attempts_updated ON login_attempts(updated_at);
