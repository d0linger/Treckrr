package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// Distinct from the application/restore and memory-heavy work leases.
const backupSchedulerLeaseKey int64 = 472019260912

// schedulerState is the cluster-wide success and retry clock for each target.
type schedulerState struct {
	volumeLast  time.Time
	volumeRetry time.Time
	s3Last      time.Time
	s3Retry     time.Time
}

// acquireSchedulerLease elects one scheduler process through PostgreSQL and
// returns a release function tied to the dedicated database connection.
func (s *Service) acquireSchedulerLease(ctx context.Context) (func() error, bool, error) {
	if s.db == nil {
		return func() error { return nil }, true, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, backupSchedulerLeaseKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() error {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.ExecContext(releaseCtx, `SELECT pg_advisory_unlock($1)`, backupSchedulerLeaseKey)
		if err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		closeErr := conn.Close()
		if err != nil {
			return err
		}
		return closeErr
	}, true, nil
}

// loadSchedulerState reads the durable cluster schedule, seeding legacy local
// success timestamps once when the database has no value yet.
func (s *Service) loadSchedulerState(ctx context.Context, fallback Status) (schedulerState, error) {
	state := schedulerState{
		volumeLast: fallback.LastBackup, volumeRetry: s.volRetryAt,
		s3Last: fallback.LastS3, s3Retry: s.s3RetryAt,
	}
	if s.db == nil {
		return state, nil
	}
	var volumeLast, volumeRetry, s3Last, s3Retry sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT volume_last_success, volume_retry_at, s3_last_success, s3_retry_at
		  FROM backup_scheduler_state WHERE id=1`).Scan(
		&volumeLast, &volumeRetry, &s3Last, &s3Retry)
	if err != nil {
		return schedulerState{}, fmt.Errorf("read backup scheduler state: %w", err)
	}
	if volumeLast.Valid {
		state.volumeLast = volumeLast.Time
	} else if !fallback.LastBackup.IsZero() {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET volume_last_success=$1, updated_at=now()
			 WHERE id=1 AND volume_last_success IS NULL`, fallback.LastBackup); err != nil {
			return schedulerState{}, fmt.Errorf("seed volume scheduler state: %w", err)
		}
	}
	if volumeRetry.Valid {
		state.volumeRetry = volumeRetry.Time
	}
	if s3Last.Valid {
		state.s3Last = s3Last.Time
	} else if !fallback.LastS3.IsZero() {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET s3_last_success=$1, updated_at=now()
			 WHERE id=1 AND s3_last_success IS NULL`, fallback.LastS3); err != nil {
			return schedulerState{}, fmt.Errorf("seed S3 scheduler state: %w", err)
		}
	}
	if s3Retry.Valid {
		state.s3Retry = s3Retry.Time
	}
	return state, nil
}

// recordSchedulerResult persists either a success clock or bounded retry time
// for one backup destination.
func (s *Service) recordSchedulerResult(ctx context.Context, destination string, success bool, now time.Time) error {
	retryAt := now.Add(statusRetryBackoff)
	switch destination {
	case "volume":
		if s.db == nil {
			if success {
				s.volRetryAt = time.Time{}
			} else {
				s.volRetryAt = retryAt
			}
			return nil
		}
		if success {
			_, err := s.db.ExecContext(ctx, `
				UPDATE backup_scheduler_state
				   SET volume_last_success=$1, volume_retry_at=NULL, updated_at=now()
				 WHERE id=1`, now)
			return err
		}
		_, err := s.db.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET volume_retry_at=$1, updated_at=now()
			 WHERE id=1`, retryAt)
		return err
	case "s3":
		if s.db == nil {
			if success {
				s.s3RetryAt = time.Time{}
			} else {
				s.s3RetryAt = retryAt
			}
			return nil
		}
		if success {
			_, err := s.db.ExecContext(ctx, `
				UPDATE backup_scheduler_state
				   SET s3_last_success=$1, s3_retry_at=NULL, updated_at=now()
				 WHERE id=1`, now)
			return err
		}
		_, err := s.db.ExecContext(ctx, `
			UPDATE backup_scheduler_state
			   SET s3_retry_at=$1, updated_at=now()
			 WHERE id=1`, retryAt)
		return err
	default:
		return fmt.Errorf("unknown backup scheduler destination %q", destination)
	}
}

// persistSchedulerResult gives scheduler bookkeeping its own short settlement
// budget. A backup deadline or disconnected HTTP client must not erase the
// durable success/failure clock after the expensive operation has completed.
func (s *Service) persistSchedulerResult(ctx context.Context, destination string, success bool, now time.Time) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.recordSchedulerResult(settleCtx, destination, success, now)
}
