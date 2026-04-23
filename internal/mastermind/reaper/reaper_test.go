package reaper

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

func TestReaper_ReclaimsStaleTasks(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(ctx, testDSN)
	defer s.Close()
	_, _ = s.Pool().Exec(ctx, "TRUNCATE TABLE tasks")

	_, _ = s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w")
	_, _ = s.Pool().Exec(ctx, `UPDATE tasks SET last_heartbeat_at = now() - interval '5 minutes' WHERE id=$1`, claimed.ID)

	r := New(s, 50*time.Millisecond, 30*time.Second, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.Run(rctx)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("reaper did not reclaim task")
		default:
		}
		got, _ := s.GetTask(ctx, claimed.ID)
		if got.Status == store.StatusPending {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
