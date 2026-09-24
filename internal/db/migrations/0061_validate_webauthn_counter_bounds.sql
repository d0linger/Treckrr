-- Validate historical rows only after migration 0058 committed and released
-- the stronger ADD CONSTRAINT lock. Do not guess or repair a corrupt counter:
-- validation must fail visibly for values outside the WebAuthn uint32 range.
ALTER TABLE webauthn_credentials
    VALIDATE CONSTRAINT webauthn_credentials_sign_count_bounds;
