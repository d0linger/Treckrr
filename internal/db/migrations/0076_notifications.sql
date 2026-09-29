-- Durable, per-user notifications. Existing accounts get the in-app center;
-- weekly mail remains explicitly opt-in so deployment cannot create new mail.
CREATE TABLE user_notification_preferences (
    user_id          BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    weekly_email     BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    dedupe_key       TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('due', 'recurring', 'import', 'mail', 'backup')),
    tone             TEXT NOT NULL CHECK (tone IN ('info', 'warn', 'bad')),
    title            TEXT NOT NULL,
    detail           TEXT NOT NULL DEFAULT '',
    href             TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at          TIMESTAMPTZ,
    dismissed_at     TIMESTAMPTZ,
    UNIQUE (user_id, dedupe_key),
    CHECK (href ~ '^/[A-Za-z0-9_?&=./%-]*$')
);
CREATE INDEX idx_notifications_user_unread
    ON notifications (user_id, created_at DESC)
    WHERE read_at IS NULL AND dismissed_at IS NULL;
