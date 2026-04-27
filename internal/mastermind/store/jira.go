package store

import (
	"context"

	"github.com/google/uuid"
)

// SetJiraURL attaches a Jira issue URL to the task the caller currently owns.
// Allowed from claimed and in_progress states. Refreshes the lease (same
// effect as a heartbeat). Returns ErrNotOwner if the caller is not the
// current claim holder or the task is in any other state.
func (s *Store) SetJiraURL(ctx context.Context, workerID string, taskID uuid.UUID, url string) error {
	states := allowedFromStrings(roleWorker, "set_jira_url")
	var urlPtr *string
	if url != "" {
		urlPtr = &url
	}
	ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET jira_url          = $3,
          last_heartbeat_at = now(),
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status = ANY($4::task_status[])
    `, taskID, workerID, urlPtr, states)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}
