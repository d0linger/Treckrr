-- Uploaded import files (bank statements, booking CSVs) held server-side
-- between the dry-run preview and the commit. The preview used to echo the raw
-- file into a hidden form field, and the percent-encoded re-post of a
-- medium-sized statement exceeded the 1 MiB body cap and failed as a
-- misleading CSRF error. The commit now names the upload by an unguessable
-- token bound to the uploading user (and, for bookings, to the billing year).
--
-- Rows are transient: the application purges them after a short TTL on every
-- new upload, and deleting a user or billing year removes theirs. Additive
-- only; no existing table or row is touched.
CREATE TABLE IF NOT EXISTS import_uploads (
    token           TEXT PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('payment', 'booking')),
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    billing_year_id BIGINT REFERENCES billing_years(id) ON DELETE CASCADE,
    content         BYTEA NOT NULL CHECK (octet_length(content) <= 4194304),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_import_uploads_created ON import_uploads (created_at);
CREATE INDEX IF NOT EXISTS idx_import_uploads_user ON import_uploads (user_id, created_at);
