-- WebAuthn signature counters are uint32 values in the protocol/library. Keep
-- future database writes inside that range without rewriting or guessing at
-- any existing value. Add NOT VALID first so the constraint protects new
-- writes immediately, then validate the existing rows explicitly below.
ALTER TABLE webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_sign_count_bounds
    CHECK (sign_count BETWEEN 0 AND 4294967295) NOT VALID;

-- Do not guess or repair a corrupted counter. Validation makes the migration
-- fail visibly if a historical/manual write is outside the protocol range.
ALTER TABLE webauthn_credentials
    VALIDATE CONSTRAINT webauthn_credentials_sign_count_bounds;
