-- Incoming receipt evidence is separate from invoice-bearing service entries.
-- Additive only: existing bookings and their photos are left untouched.
CREATE TABLE ledger_photos (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ledger_id BIGINT NOT NULL REFERENCES neighbor_ledger(id) ON DELETE CASCADE,
    image BYTEA NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'image/jpeg',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ledger_photos_ledger ON ledger_photos(ledger_id);
