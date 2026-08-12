-- Server-side WebAuthn ceremony state (SH-03). The begin→finish challenge/session
-- was previously held only in a signed, client-side cookie, so a captured cookie
-- plus assertion could be replayed (especially with counter-less authenticators).
-- Store the session here with a hard expiry; the cookie now carries only an opaque
-- ceremony id, and finish consumes the row (DELETE … RETURNING) so it is strictly
-- single-use and server-expiring.
CREATE TABLE webauthn_ceremonies (
    id           TEXT PRIMARY KEY,
    session_data BYTEA NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX webauthn_ceremonies_expires_at ON webauthn_ceremonies (expires_at);
