-- One-time recovery (backup) codes for two-factor authentication.
CREATE TABLE totp_recovery_codes (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_recovery_user ON totp_recovery_codes(user_id) WHERE used_at IS NULL;
