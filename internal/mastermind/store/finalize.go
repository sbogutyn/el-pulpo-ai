package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidTransition is returned by admin-driven transition methods when
// the task is in a state that does not allow the requested action.
var ErrInvalidTransition = errors.New("task: invalid transition for current state")

// RequestReview transitions a parked pr_opened task to review_requested.
// Returns ErrInvalidTransition if the task is in any other state, and
// ErrNotFound if the id is unknown.
func (s *Store) RequestReview(ctx context.Context, id uuid.UUID) error {
	states := allowedFromStrings(roleAdmin, "request_review")
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status     = 'review_requested',
          updated_at = now()
      WHERE id = $1 AND status = ANY($2::task_status[])
    `, id, states)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
			return ErrNotFound
		}
		return ErrInvalidTransition
	}
	return nil
}

// FinalizeTask terminates a parked task. From pr_opened or review_requested
// only: success transitions to completed, failure to failed (terminal — no
// retry from these states). Always terminal; attempt_count is not changed.
func (s *Store) FinalizeTask(ctx context.Context, id uuid.UUID, success bool, errMsg string) error {
	states := allowedFromStrings(roleAdmin, "finalize")
	var (
		targetStatus TaskStatus
		errPtr       *string
	)
	if success {
		targetStatus = StatusCompleted
	} else {
		targetStatus = StatusFailed
		if errMsg != "" {
			errPtr = &errMsg
		}
	}
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status       = $2,
          completed_at = CASE WHEN $2 = 'completed'::task_status THEN now() ELSE completed_at END,
          last_error   = COALESCE($3, last_error),
          updated_at   = now()
      WHERE id = $1 AND status = ANY($4::task_status[])
    `, id, targetStatus, errPtr, states)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
			return ErrNotFound
		}
		return ErrInvalidTransition
	}
	return nil
}
