package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DashboardWorker is one row in the agent grid: the worker as derived from the
// tasks table, the task it is currently holding (if any), and the most recent
// log lines for that task.
type DashboardWorker struct {
	Info        WorkerInfo
	CurrentTask *Task
	RecentLogs  []TaskLogEntry
}

// DashboardSnapshot is everything the /dashboard view needs in one round-trip:
// the unclaimed queue and one row per known worker.
type DashboardSnapshot struct {
	Queue       []Task
	Workers     []DashboardWorker
	GeneratedAt time.Time
}

// GetDashboard collects the queue + workers + each worker's currently-held
// task + the last `recentLogsPerWorker` log lines for that task.
//
// recentLogsPerWorker <= 0 disables the per-worker log fetch.
//
// staleAfter controls which historical workers are surfaced. ListWorkers
// returns every distinct claimed_by value ever seen in the tasks table, so
// without filtering the UI grows a long tail of ghost workers from old runs.
// When staleAfter > 0, any worker whose LastSeenAt is unknown OR older than
// (now - staleAfter) is dropped from the snapshot. Pass 0 to keep everyone.
func (s *Store) GetDashboard(
	ctx context.Context, recentLogsPerWorker int, staleAfter time.Duration,
) (DashboardSnapshot, error) {
	pending := StatusPending
	queuePage, err := s.ListTasks(ctx, ListTasksFilter{Status: &pending, Limit: 200})
	if err != nil {
		return DashboardSnapshot{}, err
	}
	// Order pending by priority desc, then created_at asc — newer ListTasks orders
	// by created_at desc; re-sort here so the rail reads top-down by importance.
	queue := append([]Task(nil), queuePage.Items...)
	sortQueueForDashboard(queue)

	workers, err := s.ListWorkers(ctx)
	if err != nil {
		return DashboardSnapshot{}, err
	}

	current, err := s.activeTasksByWorker(ctx)
	if err != nil {
		return DashboardSnapshot{}, err
	}

	now := time.Now().UTC()
	out := DashboardSnapshot{
		Queue:       queue,
		Workers:     make([]DashboardWorker, 0, len(workers)),
		GeneratedAt: now,
	}
	for _, w := range workers {
		if staleAfter > 0 && isStaleWorker(now, w.LastSeenAt, staleAfter) {
			continue
		}
		dw := DashboardWorker{Info: w}
		if t, ok := current[w.ID]; ok {
			task := t
			dw.CurrentTask = &task
			if recentLogsPerWorker > 0 {
				logs, err := s.recentTaskLogs(ctx, task.ID, recentLogsPerWorker)
				if err != nil {
					return DashboardSnapshot{}, err
				}
				dw.RecentLogs = logs
			}
		}
		out.Workers = append(out.Workers, dw)
	}
	return out, nil
}

func isStaleWorker(now time.Time, lastSeen *time.Time, staleAfter time.Duration) bool {
	if lastSeen == nil {
		return true
	}
	return now.Sub(lastSeen.UTC()) > staleAfter
}

// activeTasksByWorker returns one Task per worker that is currently holding a
// claimed/in_progress task. If a worker somehow holds more than one (shouldn't
// happen — one-claim-at-a-time is enforced at claim time), the most recently
// claimed wins.
func (s *Store) activeTasksByWorker(ctx context.Context) (map[string]Task, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT `+taskColumns+`
          FROM tasks
         WHERE claimed_by IS NOT NULL
           AND status IN ('claimed','in_progress')
         ORDER BY claimed_at DESC NULLS LAST
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if t.ClaimedBy == nil {
			continue
		}
		if _, seen := out[*t.ClaimedBy]; !seen {
			out[*t.ClaimedBy] = t
		}
	}
	return out, rows.Err()
}

func sortQueueForDashboard(ts []Task) {
	// Insertion sort — small N, stable, no extra deps.
	for i := 1; i < len(ts); i++ {
		j := i
		for j > 0 && lessQueue(ts[j], ts[j-1]) {
			ts[j], ts[j-1] = ts[j-1], ts[j]
			j--
		}
	}
}

func lessQueue(a, b Task) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

// recentTaskLogs returns the last n entries for a task, in chronological order.
// ListTaskLogs orders ASC and applies LIMIT to the head; for the dashboard we
// want the most recent n, so query DESC + reverse.
func (s *Store) recentTaskLogs(ctx context.Context, taskID uuid.UUID, n int) ([]TaskLogEntry, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT id, task_id, message, created_at
          FROM task_logs
         WHERE task_id = $1
         ORDER BY id DESC
         LIMIT $2
    `, taskID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TaskLogEntry, 0, n)
	for rows.Next() {
		var e TaskLogEntry
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Message, &e.CreatedAt); err != nil {
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
