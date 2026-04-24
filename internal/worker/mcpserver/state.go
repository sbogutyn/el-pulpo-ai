// Package mcpserver exposes the worker's claimed task to a coding agent
// running on the same machine, via the MCP protocol over a localhost HTTP
// listener. The agent uses MCP tools to claim the next task, update its
// progress note, append log lines, and mark it complete or failed.
//
// Design notes:
//
//   - The worker owns at most one claimed task at a time. Trying to claim a
//     new task while one is already active returns a clear error rather than
//     silently queueing: the agent is expected to finalize each task before
//     claiming the next.
//
//   - All state manipulation goes through [State], which serialises access so
//     the background heartbeat goroutine and incoming MCP calls cannot race.
//
//   - Finalising a task (complete or fail) releases the claim and stops the
//     heartbeat, leaving the worker idle until the agent calls claim_next_task
//     again.
package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

// Errors returned by State methods. They are converted to MCP tool errors
// by the tool layer; surfacing them as distinct sentinels makes the mapping
// straightforward.
var (
	ErrNoCurrentTask   = errors.New("mcpserver: no task currently claimed")
	ErrAlreadyHaveTask = errors.New("mcpserver: a task is already claimed; finalize it first")
	ErrTaskNotMatching = errors.New("mcpserver: supplied task id does not match the current claim")
)

// State is the worker's shared, goroutine-safe view of the currently claimed
// task. A single instance is wired into every MCP tool so multiple tool calls
// on the same task observe consistent state.
type State struct {
	client            *taskclient.Client
	heartbeatInterval time.Duration
	log               *slog.Logger

	mu      sync.Mutex
	current *taskclient.Task
	// Cancels the auto-heartbeat for the current task; nil when idle.
	stopHB func()
}

// New builds a State that claims from the given mastermind client and uses
// the given heartbeat interval for the background lease refresh.
func New(client *taskclient.Client, heartbeat time.Duration, log *slog.Logger) *State {
	if heartbeat <= 0 {
		heartbeat = 10 * time.Second
	}
	return &State{
		client:            client,
		heartbeatInterval: heartbeat,
		log:               log,
	}
}

// ClaimNext asks mastermind for the next eligible task. If one is returned,
// the caller becomes the owner until a subsequent [State.Complete] or
// [State.Fail]. Returns [ErrAlreadyHaveTask] when a claim is already active
// and [taskclient.ErrNoTask] when the queue is empty.
func (s *State) ClaimNext(ctx context.Context) (*taskclient.Task, error) {
	s.mu.Lock()
	if s.current != nil {
		t := s.current
		s.mu.Unlock()
		return t, ErrAlreadyHaveTask
	}
	s.mu.Unlock()

	// Claim outside the lock: Claim is an RPC and we don't want to hold the
	// mutex across network I/O. The window is safe: any concurrent ClaimNext
	// either saw a nil `current` above (so both may race to call Claim, but
	// only the first to re-acquire the lock wins) or will return
	// ErrAlreadyHaveTask.
	task, err := s.client.Claim(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		// A concurrent ClaimNext beat us; release this one back to the queue
		// by failing it with a "raced" message so mastermind re-queues it.
		// This is best-effort — if Fail errors we just leak the claim until
		// the reaper picks it up.
		go func(t *taskclient.Task) {
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = t.Fail(rctx, "worker.mcp: dropped due to concurrent claim race")
		}(task)
		return s.current, ErrAlreadyHaveTask
	}
	s.current = task
	s.stopHB = task.StartHeartbeat(context.Background(), s.heartbeatInterval, func(e error) {
		s.log.Warn("heartbeat failed", "task_id", task.ID(), "error", e)
	})
	return task, nil
}

// Current returns the currently claimed task, or [ErrNoCurrentTask] when
// nothing is claimed.
func (s *State) Current() (*taskclient.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, ErrNoCurrentTask
	}
	return s.current, nil
}

// Progress sets the task's overwritable progress note. The task id must match
// the current claim (or be empty to refer to it implicitly).
func (s *State) Progress(ctx context.Context, taskID, note string) error {
	t, err := s.requireTask(taskID)
	if err != nil {
		return err
	}
	return t.Progress(ctx, note)
}

// AppendLog appends one line to the task's append-only log.
func (s *State) AppendLog(ctx context.Context, taskID, message string) (int64, error) {
	t, err := s.requireTask(taskID)
	if err != nil {
		return 0, err
	}
	return t.AppendLog(ctx, message)
}

// Complete finalises the current task as successful and releases the claim.
func (s *State) Complete(ctx context.Context, taskID string) error {
	t, err := s.requireTask(taskID)
	if err != nil {
		return err
	}
	if err := t.Complete(ctx); err != nil {
		return err
	}
	s.clear()
	return nil
}

// Fail finalises the current task with the given message and releases the
// claim. Mastermind decides whether this is a retry or a terminal failure.
func (s *State) Fail(ctx context.Context, taskID, message string) error {
	t, err := s.requireTask(taskID)
	if err != nil {
		return err
	}
	if err := t.Fail(ctx, message); err != nil {
		return err
	}
	s.clear()
	return nil
}

// Release drops the in-memory claim without calling Complete/Fail on
// mastermind. Used only at worker shutdown: the server-side reaper will
// re-queue the task after its lease expires.
func (s *State) Release() {
	s.clear()
}

func (s *State) requireTask(taskID string) (*taskclient.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, ErrNoCurrentTask
	}
	if taskID != "" && taskID != s.current.ID() {
		return nil, ErrTaskNotMatching
	}
	return s.current, nil
}

func (s *State) clear() {
	s.mu.Lock()
	stop := s.stopHB
	s.stopHB = nil
	s.current = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}
