-- WebAuthn signature counters are uint32 values in the protocol/library. Keep
-- future database writes inside that range without rewriting or guessing at
-- any existing value. NOT VALID protects new writes immediately; migration
-- 0061 validates historical rows after this transaction releases its DDL lock.
ALTER TABLE webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_sign_count_bounds
    CHECK (sign_count BETWEEN 0 AND 4294967295) NOT VALID;
