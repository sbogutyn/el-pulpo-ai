package store

import (
	"context"
	"time"
)

// WorkerInfo summarizes one distinct worker identity as derived from the
// tasks table. Mastermind does not keep a dedicated workers registry; the
// only authoritative signal that a worker exists is that it has, at some
// point, claimed a task.
type WorkerInfo struct {
	ID             string
	ActiveTasks    int
	CompletedTasks int
	FailedTasks    int
	LastSeenAt     *time.Time
}

// ListWorkers returns one WorkerInfo per distinct `claimed_by` value seen in
// the tasks table, ordered by most-recently-seen first.
//
// LastSeenAt uses last_heartbeat_at when present (i.e. while a worker was
// running something and checking in), otherwise claimed_at, otherwise
// completed_at. For a worker that only has historical completed or failed
// tasks and never heartbeated, this means the completion timestamp is used
// as a best-effort "last seen" signal.
func (s *Store) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT
          claimed_by AS id,
          COUNT(*) FILTER (WHERE status IN ('claimed','in_progress'))     AS active_tasks,
          COUNT(*) FILTER (WHERE status = 'completed')                AS completed_tasks,
          COUNT(*) FILTER (WHERE status = 'failed')                   AS failed_tasks,
          MAX(COALESCE(last_heartbeat_at, claimed_at, completed_at))  AS last_seen_at
        FROM tasks
        WHERE claimed_by IS NOT NULL
        GROUP BY claimed_by
        ORDER BY last_seen_at DESC NULLS LAST, claimed_by ASC
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkerInfo
	for rows.Next() {
		var w WorkerInfo
		if err := rows.Scan(&w.ID, &w.ActiveTasks, &w.CompletedTasks, &w.FailedTasks, &w.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
