// Package runner implements the worker claim loop. It reads tasks from the
// mastermind via [taskclient], runs a (currently placeholder) handler, and
// reports progress and the final outcome back through the task's API.
package runner

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

// Handler processes a claimed task. It is given a live [*taskclient.Task]
// so it can emit progress updates with [taskclient.Task.Progress] and make
// its own heartbeat decisions if it doesn't want the auto-heartbeat.
//
// The runner drives the task's lifecycle: a nil return marks it Complete,
// a non-nil error marks it Fail with the error message. Handlers should
// NOT call Complete/Fail themselves.
type Handler func(ctx context.Context, task *taskclient.Task) error

type Config struct {
	WorkerID          string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	// WorkDuration controls the default [FakeHandler]. Handlers supplied
	// by callers are free to ignore it.
	WorkDuration time.Duration
}

type Runner struct {
	client  *taskclient.Client
	cfg     Config
	handler Handler
	log     *slog.Logger
}

// New constructs a Runner that uses [FakeHandler] by default. Use
// [Runner.SetHandler] or [NewWithHandler] to plug in real work.
func New(rpc pb.TaskServiceClient, cfg Config, log *slog.Logger) *Runner {
	return NewWithHandler(rpc, cfg, log, nil)
}

// NewWithHandler wires a Runner to a caller-supplied Handler. Passing a nil
// handler falls back to [FakeHandler].
func NewWithHandler(rpc pb.TaskServiceClient, cfg Config, log *slog.Logger, h Handler) *Runner {
	if cfg.WorkDuration == 0 {
		cfg.WorkDuration = time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	if h == nil {
		h = FakeHandler(cfg.WorkDuration)
	}
	return &Runner{
		client:  taskclient.NewClient(rpc, cfg.WorkerID),
		cfg:     cfg,
		handler: h,
		log:     log,
	}
}

// SetHandler replaces the runner's handler. Safe to call before [Runner.Run].
func (r *Runner) SetHandler(h Handler) { r.handler = h }

// Run claims tasks in a loop until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		task, err := r.client.Claim(ctx)
		if errors.Is(err, taskclient.ErrNoTask) {
			if !sleepWithJitter(ctx, r.cfg.PollInterval) {
				return
			}
			continue
		}
		if err != nil {
			r.log.Warn("claim failed", "error", err)
			if !sleepWithJitter(ctx, r.cfg.PollInterval) {
				return
			}
			continue
		}

		r.runOne(ctx, task)
	}
}

func (r *Runner) runOne(ctx context.Context, task *taskclient.Task) {
	log := r.log.With("task_id", task.ID(), "task_name", task.Name())
	log.Info("claimed")

	stopHB := task.StartHeartbeat(ctx, r.cfg.HeartbeatInterval, func(e error) {
		log.Warn("heartbeat failed", "error", e)
	})
	defer stopHB()

	workErr := r.handler(ctx, task)

	if workErr == nil {
		if err := task.Complete(ctx); err != nil {
			log.Warn("complete failed", "error", err)
			return
		}
		log.Info("completed")
		return
	}
	if err := task.Fail(ctx, workErr.Error()); err != nil {
		log.Warn("fail report failed", "error", err, "work_error", workErr)
		return
	}
	log.Info("failed", "error", workErr)
}

// FakeHandler returns a Handler that emits a single progress note and then
// sleeps for `d`. Used as the default when no real handler is plugged in.
func FakeHandler(d time.Duration) Handler {
	return func(ctx context.Context, task *taskclient.Task) error {
		_ = task.Progress(ctx, "working")
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
}

func sleepWithJitter(ctx context.Context, base time.Duration) bool {
	var jitter time.Duration
	if q := int64(base) / 4; q > 0 {
		jitter = time.Duration(rand.Int63n(q))
	}
	t := time.NewTimer(base + jitter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
