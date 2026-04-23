package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestClaimTask_ReturnsPendingTask(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})

	claimed, err := s.ClaimTask(ctx, "worker-1")
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected task, got nil")
	}
	if claimed.ID != created.ID {
		t.Errorf("wrong task: %v vs %v", claimed.ID, created.ID)
	}
	if claimed.Status != StatusClaimed {
		t.Errorf("status=%q, want claimed", claimed.Status)
	}
	if claimed.ClaimedBy == nil || *claimed.ClaimedBy != "worker-1" {
		t.Errorf("claimed_by not set")
	}
	if claimed.AttemptCount != 1 {
		t.Errorf("attempts=%d, want 1", claimed.AttemptCount)
	}
}

func TestClaimTask_EmptyQueue(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	got, err := s.ClaimTask(ctx, "worker-1")
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestClaimTask_SkipsScheduledFuture(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	future := time.Now().Add(time.Hour)
	if _, err := s.CreateTask(ctx, NewTaskInput{Name: "future", MaxAttempts: 3, ScheduledFor: &future}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimTask(ctx, "w")
	if err != nil || got != nil {
		t.Errorf("expected nil (scheduled future), got %v err=%v", got, err)
	}
}

func TestClaimTask_HonorsPriority(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	low, _ := s.CreateTask(ctx, NewTaskInput{Name: "low", Priority: 1, MaxAttempts: 3})
	hi, _ := s.CreateTask(ctx, NewTaskInput{Name: "hi", Priority: 10, MaxAttempts: 3})
	_ = low

	got, err := s.ClaimTask(ctx, "w")
	if err != nil || got == nil {
		t.Fatalf("claim: %v %v", got, err)
	}
	if got.ID != hi.ID {
		t.Errorf("expected high-priority task first")
	}
}

func TestClaimTask_ExactlyOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	const N = 100
	ids := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		tsk, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
		ids[tsk.ID.String()] = struct{}{}
	}

	var mu sync.Mutex
	claimed := make(map[string]int)
	var wg sync.WaitGroup

	const workers = 10
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				task, err := s.ClaimTask(ctx, "w")
				if err != nil {
					t.Errorf("claim err: %v", err)
					return
				}
				if task == nil {
					return
				}
				mu.Lock()
				claimed[task.ID.String()]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if len(claimed) != N {
		t.Errorf("claimed=%d distinct, want %d", len(claimed), N)
	}
	for id, count := range claimed {
		if count != 1 {
			t.Errorf("task %s claimed %d times, want 1", id, count)
		}
	}
}
