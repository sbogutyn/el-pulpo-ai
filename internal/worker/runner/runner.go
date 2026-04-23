// Package runner implements the worker claim loop and fake work.
package runner

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

type Config struct {
	WorkerID          string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	WorkDuration      time.Duration
}

type Runner struct {
	client pb.TaskServiceClient
	cfg    Config
	log    *slog.Logger
}

func New(c pb.TaskServiceClient, cfg Config, log *slog.Logger) *Runner {
	if cfg.WorkDuration == 0 {
		cfg.WorkDuration = time.Minute
	}
	return &Runner{client: c, cfg: cfg, log: log}
}

func (r *Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		task, err := r.client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: r.cfg.WorkerID})
		if status.Code(err) == codes.NotFound {
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

		r.runOne(ctx, task.Task)
	}
}

func (r *Runner) runOne(ctx context.Context, t *pb.Task) {
	log := r.log.With("task_id", t.Id, "task_name", t.Name)
	log.Info("claimed")

	hbCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.heartbeatLoop(hbCtx, t.Id, log)

	workErr := r.fakeWork(ctx)
	cancel()

	report := &pb.ReportResultRequest{WorkerId: r.cfg.WorkerID, TaskId: t.Id}
	if workErr == nil {
		report.Outcome = &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}}
	} else {
		report.Outcome = &pb.ReportResultRequest_Failure_{
			Failure: &pb.ReportResultRequest_Failure{Message: workErr.Error()},
		}
	}
	if _, err := r.client.ReportResult(ctx, report); err != nil {
		log.Warn("report failed", "error", err)
		return
	}
	log.Info("reported", "success", workErr == nil)
}

func (r *Runner) heartbeatLoop(ctx context.Context, taskID string, log *slog.Logger) {
	t := time.NewTicker(r.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.client.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: r.cfg.WorkerID, TaskId: taskID}); err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return
				}
				log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

func (r *Runner) fakeWork(ctx context.Context) error {
	t := time.NewTimer(r.cfg.WorkDuration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func sleepWithJitter(ctx context.Context, base time.Duration) bool {
	jitter := time.Duration(rand.Int63n(int64(base) / 4))
	t := time.NewTimer(base + jitter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
