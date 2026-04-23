package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusClaimed   TaskStatus = "claimed"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
)

var ErrNotFound = errors.New("task: not found")

type Task struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Payload         json.RawMessage `json:"payload"`
	Priority        int             `json:"priority"`
	Status          TaskStatus      `json:"status"`
	ScheduledFor    *time.Time      `json:"scheduled_for,omitempty"`
	AttemptCount    int             `json:"attempt_count"`
	MaxAttempts     int             `json:"max_attempts"`
	ClaimedBy       *string         `json:"claimed_by,omitempty"`
	ClaimedAt       *time.Time      `json:"claimed_at,omitempty"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	LastError       *string         `json:"last_error,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type NewTaskInput struct {
	Name         string
	Payload      json.RawMessage
	Priority     int
	MaxAttempts  int
	ScheduledFor *time.Time
}

const taskColumns = `
  id, name, payload, priority, status, scheduled_for,
  attempt_count, max_attempts,
  claimed_by, claimed_at, last_heartbeat_at,
  completed_at, last_error, created_at, updated_at
`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(
		&t.ID, &t.Name, &t.Payload, &t.Priority, &t.Status, &t.ScheduledFor,
		&t.AttemptCount, &t.MaxAttempts,
		&t.ClaimedBy, &t.ClaimedAt, &t.LastHeartbeatAt,
		&t.CompletedAt, &t.LastError, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, in NewTaskInput) (Task, error) {
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = 3
	}
	if in.Payload == nil {
		in.Payload = json.RawMessage(`{}`)
	}
	row := s.pool.QueryRow(ctx, `
      INSERT INTO tasks (name, payload, priority, max_attempts, scheduled_for)
      VALUES ($1, $2, $3, $4, $5)
      RETURNING `+taskColumns,
		in.Name, in.Payload, in.Priority, in.MaxAttempts, in.ScheduledFor,
	)
	return scanTask(row)
}

func (s *Store) GetTask(ctx context.Context, id uuid.UUID) (Task, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

type ListTasksFilter struct {
	Status *TaskStatus
	Limit  int
	Offset int
}

type TasksPage struct {
	Items []Task
	Total int
}

func (s *Store) ListTasks(ctx context.Context, f ListTasksFilter) (TasksPage, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var (
		where = ""
		args  []any
	)
	if f.Status != nil {
		where = "WHERE status = $1"
		args = append(args, *f.Status)
	}
	args = append(args, f.Limit, f.Offset)

	limitIdx := len(args) - 1
	offsetIdx := len(args)

	q := `SELECT ` + taskColumns + ` FROM tasks ` + where +
		` ORDER BY created_at DESC LIMIT $` + itoa(limitIdx) + ` OFFSET $` + itoa(offsetIdx)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return TasksPage{}, err
	}
	defer rows.Close()

	var items []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return TasksPage{}, err
		}
		items = append(items, t)
	}

	countQ := `SELECT count(*) FROM tasks ` + where
	var total int
	countArgs := []any{}
	if f.Status != nil {
		countArgs = append(countArgs, *f.Status)
	}
	if err := s.pool.QueryRow(ctx, countQ, countArgs...).Scan(&total); err != nil {
		return TasksPage{}, err
	}
	return TasksPage{Items: items, Total: total}, nil
}

func itoa(i int) string {
	// Small helper to avoid importing strconv for a single-digit placeholder index.
	return string(rune('0' + i))
}

var (
	ErrNotEditable    = errors.New("task: not editable (only pending tasks can be edited)")
	ErrNotDeletable   = errors.New("task: not deletable while active")
	ErrNotRequeueable = errors.New("task: cannot requeue while active")
)

type UpdateTaskInput struct {
	Name         string
	Priority     int
	MaxAttempts  int
	ScheduledFor *time.Time
	Payload      json.RawMessage
}

func (s *Store) UpdateTask(ctx context.Context, id uuid.UUID, in UpdateTaskInput) (Task, error) {
	row := s.pool.QueryRow(ctx, `
      UPDATE tasks
      SET name          = $2,
          priority      = $3,
          max_attempts  = $4,
          scheduled_for = $5,
          payload       = COALESCE($6, payload),
          updated_at    = now()
      WHERE id = $1 AND status = 'pending'
      RETURNING `+taskColumns,
		id, in.Name, in.Priority, in.MaxAttempts, in.ScheduledFor, in.Payload,
	)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either missing or not pending.
		if _, getErr := s.GetTask(ctx, id); getErr == ErrNotFound {
			return Task{}, ErrNotFound
		}
		return Task{}, ErrNotEditable
	}
	return t, err
}

func (s *Store) DeleteTask(ctx context.Context, id uuid.UUID) error {
	ct, err := s.pool.Exec(ctx, `
      DELETE FROM tasks
      WHERE id = $1 AND status IN ('pending','completed','failed')
    `, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		if _, getErr := s.GetTask(ctx, id); getErr == ErrNotFound {
			return ErrNotFound
		}
		return ErrNotDeletable
	}
	return nil
}

func (s *Store) RequeueTask(ctx context.Context, id uuid.UUID) (Task, error) {
	row := s.pool.QueryRow(ctx, `
      UPDATE tasks
      SET status            = 'pending',
          claimed_by        = NULL,
          claimed_at        = NULL,
          last_heartbeat_at = NULL,
          completed_at      = NULL,
          last_error        = NULL,
          attempt_count     = 0,
          scheduled_for     = NULL,
          updated_at        = now()
      WHERE id = $1 AND status IN ('pending','completed','failed')
      RETURNING `+taskColumns, id)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetTask(ctx, id); getErr == ErrNotFound {
			return Task{}, ErrNotFound
		}
		return Task{}, ErrNotRequeueable
	}
	return t, err
}
