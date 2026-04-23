package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ClaimTask atomically claims the next eligible task for the given worker.
// Returns (nil, nil) when the queue is empty.
func (s *Store) ClaimTask(ctx context.Context, workerID string) (*Task, error) {
	const q = `
      UPDATE tasks
      SET status            = 'claimed',
          claimed_by        = $1,
          claimed_at        = now(),
          last_heartbeat_at = now(),
          updated_at        = now(),
          attempt_count     = attempt_count + 1
      WHERE id = (
          SELECT id FROM tasks
          WHERE status = 'pending'
            AND (scheduled_for IS NULL OR scheduled_for <= now())
          ORDER BY priority DESC, created_at ASC
          FOR UPDATE SKIP LOCKED
          LIMIT 1
      )
      RETURNING ` + taskColumns

	row := s.pool.QueryRow(ctx, q, workerID)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
