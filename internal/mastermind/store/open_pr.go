package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrEmptyPRURL is returned by OpenPR when the supplied URL is empty.
var ErrEmptyPRURL = errors.New("task: github_pr_url is required")

// OpenPR atomically transitions the caller's claimed task from in_progress
// to pr_opened: it sets github_pr_url, clears the claim metadata
// (claimed_by, claimed_at, last_heartbeat_at), and updates updated_at.
// The caller's claim is released by this call.
func (s *Store) OpenPR(ctx context.Context, workerID string, taskID uuid.UUID, url string) error {
	if url == "" {
		return ErrEmptyPRURL
	}
	states := allowedFromStrings(roleWorker, "open_pr")
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status            = 'pr_opened',
          github_pr_url     = $3,
          claimed_by        = NULL,
          claimed_at        = NULL,
          last_heartbeat_at = NULL,
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status = ANY($4::task_status[])
    `, taskID, workerID, url, states)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}
