-- Replay protection for TOTP: remember the last accepted time-step per user so
-- a valid code cannot be reused within its ~30-90s validity window (an atomic
-- compare-and-set only accepts a strictly newer step). Nullable/additive: NULL
-- means "no code consumed yet", so the first login after this migration works.
ALTER TABLE users ADD COLUMN totp_last_step BIGINT;
