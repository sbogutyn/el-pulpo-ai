package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
//
// The returned terminal flag is true only when this call transitioned the task
// to the terminal `failed` state (attempts exhausted). It is false on success,
// on a retry (still pending), and on ErrNotOwner.
func (s *Store) ReportResult(ctx context.Context, workerID string, taskID uuid.UUID, success bool, errMsg string) (terminal bool, err error) {
	if success {
		ct, execErr := s.pool.Exec(ctx, `
          UPDATE tasks
          SET status       = 'completed',
              completed_at = now(),
              last_error   = NULL,
              updated_at   = now()
          WHERE id = $1 AND claimed_by = $2 AND status IN ('claimed','running')
        `, taskID, workerID)
		if execErr != nil {
			return false, execErr
		}
		if ct.RowsAffected() == 0 {
			return false, ErrNotOwner
		}
		return false, nil
	}

	// Failure: retry with linear backoff or terminate. RETURNING status so the
	// caller can distinguish a retry (pending) from a terminal failure (failed).
	var newStatus TaskStatus
	scanErr := s.pool.QueryRow(ctx, `
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
      RETURNING status
    `, taskID, workerID, errMsg).Scan(&newStatus)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return false, ErrNotOwner
	}
	if scanErr != nil {
		return false, scanErr
	}
	return newStatus == StatusFailed, nil
}

// ReapOutcome describes the rows touched by a single ReapStale call, split by
// the terminal status they transitioned to.
type ReapOutcome struct {
	Requeued int64
	Failed   int64
}

// ReapStale reclaims tasks whose last_heartbeat_at is older than the given
// visibility timeout. Rows whose attempt count is exhausted transition to
// `failed`; the rest are requeued as `pending` with linear backoff.
func (s *Store) ReapStale(ctx context.Context, visibility time.Duration) (ReapOutcome, error) {
	// Use microseconds so sub-second visibilities are honoured (integer
	// seconds would truncate e.g. 500ms to 0 and reap everything).
	usecs := visibility.Microseconds()
	rows, err := s.pool.Query(ctx, `
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
        AND last_heartbeat_at < now() - make_interval(secs => $1::double precision / 1000000)
      RETURNING status
    `, usecs)
	if err != nil {
		return ReapOutcome{}, err
	}
	defer rows.Close()

	var out ReapOutcome
	for rows.Next() {
		var s TaskStatus
		if err := rows.Scan(&s); err != nil {
			return ReapOutcome{}, err
		}
		if s == StatusFailed {
			out.Failed++
		} else {
			out.Requeued++
		}
	}
	if err := rows.Err(); err != nil {
		return ReapOutcome{}, err
	}
	return out, nil
}

// CountPending returns the number of pending tasks (used for the metrics gauge).
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE status = 'pending'`).Scan(&n)
	return n, err
}
