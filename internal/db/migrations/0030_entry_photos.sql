-- Photo receipts (Wiegeschein/Lieferschein) attached to a booking. Stored as a
-- re-encoded JPEG bytea in its own table so the hot entries table stays lean and
-- the image is automatically included in the DB backup. ON DELETE CASCADE so a
-- booking's photos vanish with it (including the soft-delete purge).
CREATE TABLE entry_photos (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id     BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    image        BYTEA NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'image/jpeg',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_entry_photos_entry ON entry_photos(entry_id);
