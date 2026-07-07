-- Passkeys / WebAuthn. A per-user random handle (never the DB id) identifies the
-- user to authenticators; credentials store only public keys, so nothing secret
-- lives here.
ALTER TABLE users ADD COLUMN webauthn_handle BYTEA;
-- Unique + indexed: discoverable login looks users up by handle on every attempt,
-- and no two users may share a handle.
CREATE UNIQUE INDEX idx_users_webauthn_handle ON users(webauthn_handle) WHERE webauthn_handle IS NOT NULL;

CREATE TABLE webauthn_credentials (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id BYTEA NOT NULL UNIQUE,
    public_key    BYTEA NOT NULL,
    aaguid        BYTEA,
    sign_count    BIGINT NOT NULL DEFAULT 0,
    transports    TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT 'Passkey',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);
CREATE INDEX idx_webauthn_user ON webauthn_credentials(user_id);
