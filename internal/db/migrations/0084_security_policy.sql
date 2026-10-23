-- Sign-in policy: require a second factor (TOTP or passkey) per role. Additive;
-- the single row starts with nothing required, so no account changes behavior
-- until an administrator switches the requirement on.
--
-- enforced_since moves to now() whenever a role becomes newly required, so
-- every affected account gets the full grace period from that moment (or from
-- its own creation, whichever is later).
CREATE TABLE security_policy (
    id                 INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    require_mfa_admin  BOOLEAN NOT NULL DEFAULT FALSE,
    require_mfa_editor BOOLEAN NOT NULL DEFAULT FALSE,
    grace_days         INT NOT NULL DEFAULT 7 CHECK (grace_days BETWEEN 0 AND 90),
    enforced_since     TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO security_policy (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
