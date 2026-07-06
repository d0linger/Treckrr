-- Speeds up PurgeExpiredSessions (DELETE FROM sessions WHERE expires_at < now()),
-- which runs periodically and otherwise scans the whole sessions table.
-- The other hot paths (session-token lookup via the PK, entries-by-year,
-- entries-by-neighbor-year, audit-by-created_at, unused recovery codes) are
-- already covered by existing indexes.
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
