package store

import (
	"context"
	"time"
)

// RateLimitBlocked reports whether key has reached maxFails within the active
// window. All time comparisons happen in the database to avoid app/DB clock
// drift.
func (s *Store) RateLimitBlocked(ctx context.Context, key string, maxFails int, window time.Duration) (bool, error) {
	var blocked bool
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(
		    (SELECT fails >= $2 AND now() - window_start <= make_interval(secs => $3)
		       FROM login_attempts WHERE key = $1), false)`,
		key, maxFails, window.Seconds()).Scan(&blocked)
	return blocked, err
}

// RateLimitFail records a failed attempt, starting a fresh window if the
// previous one has elapsed, and returns the fail count in the active window.
func (s *Store) RateLimitFail(ctx context.Context, key string, window time.Duration) (int, error) {
	var fails int
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO login_attempts (key, fails, window_start, updated_at)
		      VALUES ($1, 1, now(), now())
		 ON CONFLICT (key) DO UPDATE SET
		     fails = CASE WHEN now() - login_attempts.window_start > make_interval(secs => $2)
		                  THEN 1 ELSE login_attempts.fails + 1 END,
		     window_start = CASE WHEN now() - login_attempts.window_start > make_interval(secs => $2)
		                         THEN now() ELSE login_attempts.window_start END,
		     updated_at = now()
		 RETURNING fails`,
		key, window.Seconds()).Scan(&fails)
	return fails, err
}

// RateLimitReset clears the counter for key after a successful attempt.
func (s *Store) RateLimitReset(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE key = $1`, key)
	return err
}

// PurgeStaleRateLimits removes counters not touched for a day, keeping the
// table small. Called alongside session purging.
func (s *Store) PurgeStaleRateLimits(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE updated_at < now() - interval '1 day'`)
	return err
}
