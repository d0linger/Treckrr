-- Persist the WebAuthn Backup-Eligible (BE) and Backup-State (BS) flags.
-- BE is fixed when the credential is created and MUST match on every login —
-- go-webauthn rejects any mismatch ("Backup Eligible flag inconsistency"). We
-- previously didn't store it, so synced authenticators (Bitwarden, iCloud
-- Keychain, etc.) that present BE=1 failed to log in. Credentials registered
-- before this column exist with the default (false) and must be re-registered.
ALTER TABLE webauthn_credentials ADD COLUMN backup_eligible BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE webauthn_credentials ADD COLUMN backup_state    BOOLEAN NOT NULL DEFAULT FALSE;
