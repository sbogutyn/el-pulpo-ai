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
	s, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := s.ClaimTask(ctx, "w")
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimTask: expected task, got nil")
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE tasks SET last_heartbeat_at = now() - interval '5 minutes' WHERE id=$1`, claimed.ID); err != nil {
		t.Fatalf("rewind heartbeat: %v", err)
	}

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
		got, err := s.GetTask(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.Status == store.StatusPending {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
