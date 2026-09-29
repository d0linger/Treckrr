-- Personal filter views are operator preferences, not accounting data. They
-- are isolated by user and disappear automatically with that account.
CREATE TABLE saved_views (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      TEXT NOT NULL CHECK (scope IN ('bookings')),
    name       TEXT NOT NULL,
    query      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, scope, name)
);

CREATE INDEX idx_saved_views_user_scope ON saved_views (user_id, scope, name);
