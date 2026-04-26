package store

import (
	"context"
	"testing"
	"time"
)

func TestGetDashboard_QueueOrderedByPriority(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	low, _ := s.CreateTask(ctx, NewTaskInput{Name: "low", Priority: 1})
	high, _ := s.CreateTask(ctx, NewTaskInput{Name: "high", Priority: 10})
	mid, _ := s.CreateTask(ctx, NewTaskInput{Name: "mid", Priority: 5})

	snap, err := s.GetDashboard(ctx, 0, 0)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	gotIDs := []string{}
	for _, q := range snap.Queue {
		gotIDs = append(gotIDs, q.Name)
	}
	if len(gotIDs) != 3 {
		t.Fatalf("queue len=%d, want 3 (got %v)", len(gotIDs), gotIDs)
	}
	want := []string{"high", "mid", "low"}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("queue[%d]=%s, want %s (full=%v)", i, gotIDs[i], want[i], gotIDs)
		}
	}
	_ = low
	_ = high
	_ = mid
}

func TestGetDashboard_AssignsCurrentTaskAndLogs(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "do-thing", Priority: 5})
	claimed, err := s.ClaimTask(ctx, "worker-A")
	if err != nil || claimed == nil || claimed.ID != created.ID {
		t.Fatalf("setup claim: %v %v", err, claimed)
	}
	for _, msg := range []string{"step 1", "step 2", "step 3"} {
		if _, err := s.AppendTaskLog(ctx, "worker-A", claimed.ID, msg); err != nil {
			t.Fatalf("AppendTaskLog: %v", err)
		}
	}

	snap, err := s.GetDashboard(ctx, 2, 0)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}

	if len(snap.Queue) != 0 {
		t.Errorf("queue should be empty after claim, got %d", len(snap.Queue))
	}
	if len(snap.Workers) != 1 {
		t.Fatalf("workers=%d, want 1", len(snap.Workers))
	}
	w := snap.Workers[0]
	if w.Info.ID != "worker-A" {
		t.Errorf("worker id=%q", w.Info.ID)
	}
	if w.CurrentTask == nil || w.CurrentTask.ID != created.ID {
		t.Errorf("CurrentTask mismatch: %+v", w.CurrentTask)
	}
	if len(w.RecentLogs) != 2 {
		t.Errorf("RecentLogs=%d, want 2 (limit applied)", len(w.RecentLogs))
	}
	if len(w.RecentLogs) >= 2 && (w.RecentLogs[0].Message != "step 2" || w.RecentLogs[1].Message != "step 3") {
		t.Errorf("RecentLogs should be the tail: %+v", w.RecentLogs)
	}
}

func TestGetDashboard_IdleWorkerHasNoCurrent(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "one-shot"})
	claimed, _ := s.ClaimTask(ctx, "worker-Z")
	if claimed == nil {
		t.Fatal("expected claim")
	}
	if _, err := s.ReportResult(ctx, "worker-Z", claimed.ID, true, ""); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	snap, err := s.GetDashboard(ctx, 5, 0)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if len(snap.Workers) != 1 {
		t.Fatalf("workers=%d, want 1", len(snap.Workers))
	}
	w := snap.Workers[0]
	if w.CurrentTask != nil {
		t.Errorf("CurrentTask should be nil after completion, got %+v", w.CurrentTask)
	}
	if w.Info.CompletedTasks != 1 {
		t.Errorf("CompletedTasks=%d, want 1", w.Info.CompletedTasks)
	}
	_ = created
}

func TestGetDashboard_StaleAfterDropsOldWorkers(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	// One "fresh" worker that just completed a task — its last_seen_at is now.
	if _, err := s.CreateTask(ctx, NewTaskInput{Name: "fresh-task"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	freshClaim, _ := s.ClaimTask(ctx, "worker-fresh")
	if freshClaim == nil {
		t.Fatal("expected fresh claim")
	}
	if _, err := s.ReportResult(ctx, "worker-fresh", freshClaim.ID, true, ""); err != nil {
		t.Fatalf("ReportResult fresh: %v", err)
	}

	// One "stale" worker whose only task we backdate so its derived last_seen_at
	// falls outside the window.
	staleTask, _ := s.CreateTask(ctx, NewTaskInput{Name: "stale-task"})
	staleClaim, _ := s.ClaimTask(ctx, "worker-stale")
	if staleClaim == nil {
		t.Fatal("expected stale claim")
	}
	if _, err := s.ReportResult(ctx, "worker-stale", staleClaim.ID, true, ""); err != nil {
		t.Fatalf("ReportResult stale: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
        UPDATE tasks
           SET claimed_at        = now() - interval '3 days',
               last_heartbeat_at = now() - interval '3 days',
               completed_at      = now() - interval '3 days'
         WHERE id = $1
    `, staleTask.ID); err != nil {
		t.Fatalf("backdate stale: %v", err)
	}

	// staleAfter=0 → both workers visible.
	all, err := s.GetDashboard(ctx, 0, 0)
	if err != nil {
		t.Fatalf("GetDashboard(0): %v", err)
	}
	if len(all.Workers) != 2 {
		t.Errorf("with staleAfter=0 want 2 workers, got %d", len(all.Workers))
	}

	// staleAfter=1h → only the fresh worker should remain.
	filtered, err := s.GetDashboard(ctx, 0, time.Hour)
	if err != nil {
		t.Fatalf("GetDashboard(1h): %v", err)
	}
	if len(filtered.Workers) != 1 {
		t.Fatalf("with staleAfter=1h want 1 worker, got %d (%+v)", len(filtered.Workers), filtered.Workers)
	}
	if filtered.Workers[0].Info.ID != "worker-fresh" {
		t.Errorf("expected the fresh worker to survive, got %q", filtered.Workers[0].Info.ID)
	}
}
