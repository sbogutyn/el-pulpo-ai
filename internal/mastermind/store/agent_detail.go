package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrAgentNotFound is returned by [Store.GetAgentDetail] when no task in the
// `tasks` table has ever been claimed by the requested worker id. There is
// no separate worker registry, so "agent exists" means "has at least one
// row pointing at it via claimed_by".
var ErrAgentNotFound = errors.New("agent: not found")

// AgentLog is one task_logs row enriched with the parent task's name and
// short id so the agent-detail view can show context per line without an
// extra round-trip per row.
type AgentLog struct {
	TaskLogEntry
	TaskName string
}

// AgentRecentTask is a compact summary of one task an agent has touched —
// enough to render in a sidebar list with status, when, and a link to the
// full /tasks/{id} page.
type AgentRecentTask struct {
	Task
	// LastActivity is the most recent of claimed_at / completed_at — used
	// for "Nm ago" rendering. Zero when neither is set, which shouldn't
	// happen for a row returned by this query.
	LastActivity time.Time
}

// AgentDetail bundles everything the agent-detail view needs in one snapshot.
type AgentDetail struct {
	Info        WorkerInfo
	CurrentTask *Task
	RecentTasks []AgentRecentTask
	Logs        []AgentLog
	GeneratedAt time.Time
}

// GetAgentDetail returns metadata + recent tasks + a tail of aggregated logs
// for one worker. Returns [ErrAgentNotFound] if no rows reference the worker.
//
// recentTasksLimit and logTailLimit are upper bounds; pass <=0 to use sane
// defaults (10 and 500 respectively).
func (s *Store) GetAgentDetail(
	ctx context.Context, workerID string, recentTasksLimit, logTailLimit int,
) (AgentDetail, error) {
	if recentTasksLimit <= 0 {
		recentTasksLimit = 10
	}
	if logTailLimit <= 0 {
		logTailLimit = 500
	}

	info, err := s.workerInfo(ctx, workerID)
	if err != nil {
		return AgentDetail{}, err
	}

	current, err := s.workerCurrentTask(ctx, workerID)
	if err != nil {
		return AgentDetail{}, err
	}

	recent, err := s.workerRecentTasks(ctx, workerID, recentTasksLimit)
	if err != nil {
		return AgentDetail{}, err
	}

	logs, err := s.workerLogTail(ctx, workerID, logTailLimit)
	if err != nil {
		return AgentDetail{}, err
	}

	return AgentDetail{
		Info:        info,
		CurrentTask: current,
		RecentTasks: recent,
		Logs:        logs,
		GeneratedAt: time.Now().UTC(),
	}, nil
}

// workerInfo runs the same aggregation as ListWorkers but for one worker.
// Returns ErrAgentNotFound when no tasks have ever been claimed by the id.
func (s *Store) workerInfo(ctx context.Context, workerID string) (WorkerInfo, error) {
	row := s.pool.QueryRow(ctx, `
        SELECT
          claimed_by AS id,
          COUNT(*) FILTER (WHERE status IN ('claimed','in_progress'))     AS active_tasks,
          COUNT(*) FILTER (WHERE status = 'completed')                AS completed_tasks,
          COUNT(*) FILTER (WHERE status = 'failed')                   AS failed_tasks,
          MAX(COALESCE(last_heartbeat_at, claimed_at, completed_at))  AS last_seen_at
        FROM tasks
        WHERE claimed_by = $1
        GROUP BY claimed_by
    `, workerID)
	var w WorkerInfo
	if err := row.Scan(&w.ID, &w.ActiveTasks, &w.CompletedTasks, &w.FailedTasks, &w.LastSeenAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkerInfo{}, ErrAgentNotFound
		}
		return WorkerInfo{}, err
	}
	return w, nil
}

func (s *Store) workerCurrentTask(ctx context.Context, workerID string) (*Task, error) {
	row := s.pool.QueryRow(ctx, `
        SELECT `+taskColumns+`
          FROM tasks
         WHERE claimed_by = $1
           AND status IN ('claimed','in_progress')
         ORDER BY claimed_at DESC NULLS LAST
         LIMIT 1
    `, workerID)
	t, err := scanTask(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (s *Store) workerRecentTasks(ctx context.Context, workerID string, limit int) ([]AgentRecentTask, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT `+taskColumns+`,
               GREATEST(COALESCE(completed_at, 'epoch'::timestamptz),
                        COALESCE(claimed_at,   'epoch'::timestamptz)) AS last_activity
          FROM tasks
         WHERE claimed_by = $1
         ORDER BY last_activity DESC
         LIMIT $2
    `, workerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AgentRecentTask, 0, limit)
	for rows.Next() {
		var t Task
		var last time.Time
		err := rows.Scan(
			&t.ID, &t.Name, &t.Payload, &t.Priority, &t.Status, &t.ScheduledFor,
			&t.AttemptCount, &t.MaxAttempts,
			&t.ClaimedBy, &t.ClaimedAt, &t.LastHeartbeatAt,
			&t.CompletedAt, &t.LastError, &t.ProgressNote,
			&t.JiraURL, &t.GithubPRURL,
			&t.CreatedAt, &t.UpdatedAt,
			&last,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, AgentRecentTask{Task: t, LastActivity: last})
	}
	return out, rows.Err()
}

// workerLogTail returns the most recent logTailLimit log lines across all
// tasks ever claimed by the worker, in chronological (oldest-first) order so
// the UI can scroll naturally with newest at the bottom.
func (s *Store) workerLogTail(ctx context.Context, workerID string, limit int) ([]AgentLog, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT tl.id, tl.task_id, tl.message, tl.created_at, t.name
          FROM task_logs tl
          JOIN tasks t ON t.id = tl.task_id
         WHERE t.claimed_by = $1
         ORDER BY tl.id DESC
         LIMIT $2
    `, workerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AgentLog, 0, limit)
	for rows.Next() {
		var e AgentLog
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Message, &e.CreatedAt, &e.TaskName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

