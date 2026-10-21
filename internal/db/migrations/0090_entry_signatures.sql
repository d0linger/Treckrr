-- On-site signature for a booking. Additive.
--
-- The cheapest dispute is the one settled at the field edge: the neighbor
-- confirms the work on the operator's phone. One signature per booking, kept
-- as a re-encoded PNG next to the booking (like entry_photos, so the backup
-- carries it) with the signer's name, the device time of signing, the server
-- time it arrived and the booking summary it confirmed. It is proof of
-- agreement, not a qualified electronic signature.
CREATE TABLE entry_signatures (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id       BIGINT NOT NULL UNIQUE REFERENCES entries(id) ON DELETE CASCADE,
    signer_name    TEXT NOT NULL CHECK (length(btrim(signer_name)) BETWEEN 1 AND 100),
    signed_at      TIMESTAMPTZ NOT NULL,
    received_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    image          BYTEA NOT NULL CHECK (octet_length(image) BETWEEN 1 AND 1048576),
    confirmed_text TEXT NOT NULL CHECK (length(confirmed_text) <= 500),
    created_by     TEXT NOT NULL DEFAULT ''
);
