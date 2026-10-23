-- Calendar subscription (ICS). Additive.
--
-- Due dates (open invoices, installments, dunning deadlines, next series runs)
-- lived only inside the app. One revocable feed per user publishes them as an
-- iCalendar file for any phone calendar. Only the SHA-256 of the bearer token
-- is stored; minimal=true leaves names and amounts out of the events, for
-- people who subscribe through an online calendar that fetches the feed.
CREATE TABLE calendar_feeds (
    user_id      BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    minimal      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
