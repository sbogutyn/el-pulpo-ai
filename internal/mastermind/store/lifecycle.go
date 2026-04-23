package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNotOwner = errors.New("task: caller does not own this claim")

func (s *Store) Heartbeat(ctx context.Context, workerID string, taskID uuid.UUID) error {
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET last_heartbeat_at = now(),
          status            = CASE WHEN status = 'claimed' THEN 'running' ELSE status END,
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status IN ('claimed','running')
    `, taskID, workerID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}

// ReportResult finalises a task the worker owns.
//
//	success = true  -> completed
//	success = false -> retry (reset to pending with backoff) or failed (attempts exhausted)
func (s *Store) ReportResult(ctx context.Context, workerID string, taskID uuid.UUID, success bool, errMsg string) error {
	if success {
		ct, err := s.pool.Exec(ctx, `
          UPDATE tasks
          SET status       = 'completed',
              completed_at = now(),
              last_error   = NULL,
              updated_at   = now()
          WHERE id = $1 AND claimed_by = $2 AND status IN ('claimed','running')
        `, taskID, workerID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return ErrNotOwner
		}
		return nil
	}

	// Failure: retry with linear backoff or terminate.
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status            = CASE
                                WHEN attempt_count >= max_attempts THEN 'failed'::task_status
                                ELSE 'pending'::task_status
                              END,
          scheduled_for     = CASE
                                WHEN attempt_count >= max_attempts THEN scheduled_for
                                ELSE now() + make_interval(secs => attempt_count * 30)
                              END,
          claimed_by        = CASE
                                WHEN attempt_count >= max_attempts THEN claimed_by
                                ELSE NULL
                              END,
          claimed_at        = CASE
                                WHEN attempt_count >= max_attempts THEN claimed_at
                                ELSE NULL
                              END,
          last_heartbeat_at = CASE
                                WHEN attempt_count >= max_attempts THEN last_heartbeat_at
                                ELSE NULL
                              END,
          last_error        = $3,
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status IN ('claimed','running')
    `, taskID, workerID, errMsg)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}

// ReapStale reclaims tasks whose last_heartbeat_at is older than the given
// visibility timeout. Returns the number of rows affected.
func (s *Store) ReapStale(ctx context.Context, visibility time.Duration) (int64, error) {
	secs := int(visibility.Seconds())
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status            = CASE
                                WHEN attempt_count >= max_attempts THEN 'failed'::task_status
                                ELSE 'pending'::task_status
                              END,
          scheduled_for     = CASE
                                WHEN attempt_count >= max_attempts THEN scheduled_for
                                ELSE now() + make_interval(secs => attempt_count * 30)
                              END,
          claimed_by        = CASE
                                WHEN attempt_count >= max_attempts THEN claimed_by
                                ELSE NULL
                              END,
          claimed_at        = CASE
                                WHEN attempt_count >= max_attempts THEN claimed_at
                                ELSE NULL
                              END,
          last_heartbeat_at = CASE
                                WHEN attempt_count >= max_attempts THEN last_heartbeat_at
                                ELSE NULL
                              END,
          last_error        = 'lease expired',
          updated_at        = now()
      WHERE status IN ('claimed','running')
        AND last_heartbeat_at < now() - make_interval(secs => $1)
    `, secs)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// CountPending returns the number of pending tasks (used for the metrics gauge).
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE status = 'pending'`).Scan(&n)
	return n, err
}
