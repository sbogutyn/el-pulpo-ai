// Package reaper reclaims tasks whose heartbeat exceeds the visibility timeout.
package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/metrics"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

type Reaper struct {
	store      *store.Store
	interval   time.Duration
	visibility time.Duration
	log        *slog.Logger
}

func New(s *store.Store, interval, visibility time.Duration, log *slog.Logger) *Reaper {
	return &Reaper{store: s, interval: interval, visibility: visibility, log: log}
}

// Run ticks until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			outcome, err := r.store.ReapStale(ctx, r.visibility)
			if err != nil {
				r.log.Warn("reaper: reap failed", "error", err)
				continue
			}
			if total := outcome.Requeued + outcome.Failed; total > 0 {
				metrics.TasksReapedTotal.Add(float64(total))
				if outcome.Failed > 0 {
					metrics.TasksFailedTotal.WithLabelValues("reaped").Add(float64(outcome.Failed))
				}
				r.log.Info("reaper: reclaimed stale tasks", "requeued", outcome.Requeued, "failed", outcome.Failed)
			}
		}
	}
}
