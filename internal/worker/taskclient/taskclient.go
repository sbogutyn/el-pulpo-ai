// Package taskclient is the worker-facing wrapper around the mastermind
// gRPC TaskService. It gives workers an explicit, object-oriented handle to a
// claimed task instead of raw RPC calls: callers hold a [*Task] between Claim
// and Complete/Fail and invoke methods on it to heartbeat, report progress,
// or finalize the work.
package taskclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// ErrNoTask is returned by [Client.Claim] when the mastermind queue is empty.
// Callers should treat this as a benign "try again later" signal.
var ErrNoTask = errors.New("taskclient: no tasks available")

// ErrAlreadyFinalized is returned by [Task.Complete] / [Task.Fail] if the
// task was already finalized in this process.
var ErrAlreadyFinalized = errors.New("taskclient: task already finalized")

// Client is a thin wrapper over a [pb.TaskServiceClient] that knows the local
// worker_id, so callers don't have to thread it through every call.
type Client struct {
	rpc      pb.TaskServiceClient
	workerID string
}

// NewClient binds an RPC client to a worker_id.
func NewClient(rpc pb.TaskServiceClient, workerID string) *Client {
	return &Client{rpc: rpc, workerID: workerID}
}

// WorkerID returns the worker id this client identifies as.
func (c *Client) WorkerID() string { return c.workerID }

// Claim requests the next eligible task from mastermind. Returns
// [ErrNoTask] when the queue is empty.
func (c *Client) Claim(ctx context.Context) (*Task, error) {
	resp, err := c.rpc.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: c.workerID})
	if status.Code(err) == codes.NotFound {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, err
	}
	t := resp.GetTask()
	if t == nil {
		return nil, fmt.Errorf("taskclient: server returned empty task")
	}
	return &Task{
		id:       t.GetId(),
		name:     t.GetName(),
		payload:  t.GetPayload(),
		workerID: c.workerID,
		rpc:      c.rpc,
	}, nil
}

// Task is a claimed unit of work. It owns the caller's lease until either
// [Task.Complete] or [Task.Fail] is invoked; once finalized the task is
// inert and further lifecycle calls return [ErrAlreadyFinalized].
//
// Task is safe for concurrent use: a background heartbeat goroutine (see
// [Task.StartHeartbeat]) may run alongside user calls to Progress.
type Task struct {
	id       string
	name     string
	payload  []byte
	workerID string
	rpc      pb.TaskServiceClient

	mu        sync.Mutex
	finalized bool
	hbCancel  context.CancelFunc
	hbStopped chan struct{}
}

// ID returns the task's UUID string assigned by mastermind.
func (t *Task) ID() string { return t.id }

// Name returns the task's logical type (the dispatch key).
func (t *Task) Name() string { return t.name }

// Payload returns the opaque JSON payload bytes. The slice is aliased into
// the RPC response; callers that mutate it should copy first.
func (t *Task) Payload() []byte { return t.payload }

// Heartbeat refreshes the caller's lease. This is equivalent to a no-note
// [Task.Progress] call. Returns an error if the task no longer owns the
// claim (e.g. reaped by the mastermind reaper).
func (t *Task) Heartbeat(ctx context.Context) error {
	_, err := t.rpc.Heartbeat(ctx, &pb.HeartbeatRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
	})
	return err
}

// Progress attaches a short human-readable note to the task (overwriting any
// previous note) and refreshes the lease. Passing the empty string clears
// the note.
func (t *Task) Progress(ctx context.Context, note string) error {
	_, err := t.rpc.UpdateProgress(ctx, &pb.UpdateProgressRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
		Note:     note,
	})
	return err
}

// AppendLog appends one line to the task's append-only log. Unlike
// [Task.Progress], which overwrites a single "current status" note, AppendLog
// is used to record a narrative trail of what the worker did. AppendLog also
// refreshes the lease. Returns the server-assigned row id.
func (t *Task) AppendLog(ctx context.Context, message string) (int64, error) {
	resp, err := t.rpc.AppendLog(ctx, &pb.AppendLogRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
		Message:  message,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetId(), nil
}

// SetJiraURL attaches a JIRA URL to the task and refreshes the lease.
// Allowed any time the worker holds the claim (claimed or in_progress).
func (t *Task) SetJiraURL(ctx context.Context, url string) error {
	_, err := t.rpc.SetJiraURL(ctx, &pb.SetJiraURLRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
		Url:      url,
	})
	return err
}

// OpenPR atomically transitions the task to pr_opened, sets github_pr_url,
// and releases the caller's claim. After this call the Task is "finalized"
// in the same sense as Complete/Fail: subsequent lifecycle calls return
// ErrAlreadyFinalized. The auto-heartbeat (if running) is stopped.
func (t *Task) OpenPR(ctx context.Context, githubPRURL string) error {
	if err := t.markFinalized(); err != nil {
		return err
	}
	_, err := t.rpc.OpenPR(ctx, &pb.OpenPRRequest{
		WorkerId:    t.workerID,
		TaskId:      t.id,
		GithubPrUrl: githubPRURL,
	})
	return err
}

// Complete finalizes the task as successful. Subsequent calls return
// [ErrAlreadyFinalized]. Any running auto-heartbeat is stopped before the
// RPC so it can't race the server-side terminal transition.
func (t *Task) Complete(ctx context.Context) error {
	if err := t.markFinalized(); err != nil {
		return err
	}
	_, err := t.rpc.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
		Outcome:  &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
	return err
}

// Fail finalizes the task as failed with the given human-readable message.
// Mastermind decides whether this is a retry or a terminal failure based on
// the task's attempt budget.
func (t *Task) Fail(ctx context.Context, msg string) error {
	if err := t.markFinalized(); err != nil {
		return err
	}
	_, err := t.rpc.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: t.workerID,
		TaskId:   t.id,
		Outcome:  &pb.ReportResultRequest_Failure_{Failure: &pb.ReportResultRequest_Failure{Message: msg}},
	})
	return err
}

// StartHeartbeat spawns a goroutine that heartbeats every `interval` using
// the given parent context. The returned stop function is idempotent and
// waits for the goroutine to exit, so it is safe to call in a defer.
//
// The heartbeat loop logs nothing itself; errors are reported by calling
// `onError` when non-nil. A typical caller just ignores transient errors,
// since a persistent failure will cause the lease to expire and the next
// Complete/Fail to return FAILED_PRECONDITION.
func (t *Task) StartHeartbeat(parent context.Context, interval time.Duration, onError func(error)) func() {
	t.mu.Lock()
	if t.hbCancel != nil {
		t.mu.Unlock()
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	stopped := make(chan struct{})
	t.hbCancel = cancel
	t.hbStopped = stopped
	t.mu.Unlock()

	go func() {
		defer close(stopped)
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if err := t.Heartbeat(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					if onError != nil {
						onError(err)
					}
				}
			}
		}
	}()

	return func() {
		t.mu.Lock()
		cancel := t.hbCancel
		done := t.hbStopped
		t.hbCancel = nil
		t.hbStopped = nil
		t.mu.Unlock()
		if cancel == nil {
			return
		}
		cancel()
		<-done
	}
}

func (t *Task) markFinalized() error {
	t.mu.Lock()
	if t.finalized {
		t.mu.Unlock()
		return ErrAlreadyFinalized
	}
	t.finalized = true
	cancel := t.hbCancel
	done := t.hbStopped
	t.hbCancel = nil
	t.hbStopped = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}
