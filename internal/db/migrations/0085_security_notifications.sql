-- Security notifications. Additive.
--
-- A new passkey, a disabled second factor, a password reset or a sign-in from an
-- unfamiliar browser used to happen without the account holder hearing about
-- it. Such events now reach the affected user as notifications (kind
-- 'security') and, when mail is configured and the user has an address, as an
-- outbox mail.
--
-- user_known_devices remembers a coarse browser/OS label per user (no IP, no
-- full user agent), so a sign-in from a combination never seen before can be
-- told apart from the usual one.
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
    CHECK (kind IN ('due', 'recurring', 'import', 'mail', 'backup', 'feedback', 'security'));

CREATE TABLE user_known_devices (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device     TEXT NOT NULL CHECK (char_length(device) BETWEEN 1 AND 100),
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device)
);
