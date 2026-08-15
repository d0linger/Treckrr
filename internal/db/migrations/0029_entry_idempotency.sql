-- Idempotency key for offline-captured bookings: the client tags each booking
-- with a UUID and replays it when the connection returns. A unique key makes a
-- double-replay safe (the second insert is a no-op), so a flaky reconnect can't
-- double-book. Nullable + unique-when-present, so normal online bookings (no key)
-- are unaffected.
ALTER TABLE entries ADD COLUMN idempotency_key TEXT;
CREATE UNIQUE INDEX idx_entries_idempotency ON entries(idempotency_key) WHERE idempotency_key IS NOT NULL;
