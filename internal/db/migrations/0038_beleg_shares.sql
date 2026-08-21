-- 0038: self-service public Beleg links (Parkrr portal-link pattern).
--
-- The share token becomes a stored, random credential instead of a stateless
-- HMAC: only its SHA-256 hash is persisted (the raw token exists once, in the
-- creator's browser), so a DB leak exposes no usable links. A stored row is
-- what makes SELF-SERVICE possible: the owner picks a validity (Linkdauer) at
-- creation and can revoke (delete) a link at any time — both impossible with
-- the previous self-expiring HMAC tokens. Legacy HMAC links keep verifying
-- until they expire (server-side fallback); no data migration needed.
-- Revocation is a hard DELETE (the audit log is the durable record), so
-- expiry is the only liveness condition — no soft-delete flag to keep in sync.
CREATE TABLE beleg_shares (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash      TEXT NOT NULL UNIQUE,
    neighbor_id     BIGINT NOT NULL REFERENCES neighbors(id) ON DELETE CASCADE,
    billing_year_id BIGINT NOT NULL REFERENCES billing_years(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ
);

-- The Beleg page lists a neighbor+year's active links.
CREATE INDEX beleg_shares_by_beleg ON beleg_shares (neighbor_id, billing_year_id);
