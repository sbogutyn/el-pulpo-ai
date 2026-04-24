package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TaskLogEntry is a single row of the `task_logs` append-only log.
type TaskLogEntry struct {
	ID        int64     `json:"id"`
	TaskID    uuid.UUID `json:"task_id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// AppendTaskLog inserts one log entry for the given task, provided the caller
// is the current claim holder. The insert is gated by a sub-select against
// `tasks` so a reaped / stolen claim cannot continue writing into the log.
// Also refreshes the lease (last_heartbeat_at), matching UpdateProgress.
//
// Returns ErrNotOwner when the caller is not the current claim holder and
// ErrNotFound when the task id is unknown.
func (s *Store) AppendTaskLog(
	ctx context.Context, workerID string, taskID uuid.UUID, message string,
) (TaskLogEntry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TaskLogEntry{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback on error path

	var owner *string
	var st TaskStatus
	err = tx.QueryRow(ctx,
		`SELECT claimed_by, status FROM tasks WHERE id = $1`,
		taskID,
	).Scan(&owner, &st)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskLogEntry{}, ErrNotFound
	}
	if err != nil {
		return TaskLogEntry{}, err
	}
	if owner == nil || *owner != workerID || (st != StatusClaimed && st != StatusRunning) {
		return TaskLogEntry{}, ErrNotOwner
	}

	var entry TaskLogEntry
	err = tx.QueryRow(ctx, `
      INSERT INTO task_logs (task_id, message)
      VALUES ($1, $2)
      RETURNING id, task_id, message, created_at
    `, taskID, message).Scan(&entry.ID, &entry.TaskID, &entry.Message, &entry.CreatedAt)
	if err != nil {
		return TaskLogEntry{}, err
	}

	if _, err := tx.Exec(ctx, `
      UPDATE tasks
      SET last_heartbeat_at = now(),
          status            = CASE WHEN status = 'claimed' THEN 'running' ELSE status END,
          updated_at        = now()
      WHERE id = $1
    `, taskID); err != nil {
		return TaskLogEntry{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return TaskLogEntry{}, err
	}
	return entry, nil
}

// ListTaskLogs returns log entries for a task in chronological order. A
// non-positive limit means "no cap"; callers that hand a user-supplied
// value should clamp it themselves.
func (s *Store) ListTaskLogs(ctx context.Context, taskID uuid.UUID, limit int) ([]TaskLogEntry, error) {
	q := `SELECT id, task_id, message, created_at
	        FROM task_logs
	       WHERE task_id = $1
	       ORDER BY id ASC`
	args := []any{taskID}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TaskLogEntry
	for rows.Next() {
		var e TaskLogEntry
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
