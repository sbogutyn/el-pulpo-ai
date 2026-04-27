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
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE"); err != nil {
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

func TestReapStale_DoesNotReapParkedStates(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := s.ClaimTask(ctx, "w1")
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTask: claimed=%v err=%v", claimed, err)
	}
	if err := s.Heartbeat(ctx, "w1", claimed.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}

	// OpenPR clears last_heartbeat_at, but force a stale value to prove the
	// reaper's status filter is what guards parked tasks (not the NULL alone).
	if _, err := s.Pool().Exec(ctx,
		`UPDATE tasks SET last_heartbeat_at = now() - interval '1 hour' WHERE id = $1`,
		claimed.ID,
	); err != nil {
		t.Fatal(err)
	}

	out, err := s.ReapStale(ctx, time.Second)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if out.Requeued != 0 || out.Failed != 0 {
		t.Errorf("reaped a parked task: %+v", out)
	}
	got, err := s.GetTask(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.StatusPROpened {
		t.Errorf("status=%q, want pr_opened", got.Status)
	}
}
