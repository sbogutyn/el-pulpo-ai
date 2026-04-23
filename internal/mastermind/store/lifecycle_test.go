package store

import (
	"context"
	"testing"
	"time"
)

func TestHeartbeat_TransitionsClaimedToRunning(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = created

	if err := s.Heartbeat(ctx, "w1", claimed.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusRunning {
		t.Errorf("status=%q, want running", got.Status)
	}
}

func TestHeartbeat_WrongOwnerFailsPrecondition(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	err := s.Heartbeat(ctx, "w2", claimed.ID)
	if err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner", err)
	}
}

func TestReportResult_Success(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	if _, err := s.ReportResult(ctx, "w1", claimed.ID, true, ""); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusCompleted {
		t.Errorf("status=%q, want completed", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at not set")
	}
}

func TestReportResult_FailureRetriesThenFails(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	// max_attempts=2 so we can exhaust in two attempts.
	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 2})

	claim1, _ := s.ClaimTask(ctx, "w")
	if _, err := s.ReportResult(ctx, "w", claim1.ID, false, "bad"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, claim1.ID)
	if got.Status != StatusPending {
		t.Errorf("after first failure, status=%q, want pending (retry)", got.Status)
	}
	if got.ScheduledFor == nil || time.Until(*got.ScheduledFor) <= 0 {
		t.Errorf("scheduled_for not in future: %v", got.ScheduledFor)
	}
	if got.LastError == nil || *got.LastError != "bad" {
		t.Errorf("last_error not recorded")
	}

	// Force scheduled_for to the past, claim, fail again.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET scheduled_for = now() - interval '1 hour' WHERE id=$1`, claim1.ID); err != nil {
		t.Fatal(err)
	}
	claim2, err := s.ClaimTask(ctx, "w")
	if err != nil || claim2 == nil {
		t.Fatalf("second claim failed: %v %v", claim2, err)
	}
	if _, err := s.ReportResult(ctx, "w", claim2.ID, false, "bad2"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTask(ctx, claim1.ID)
	if got.Status != StatusFailed {
		t.Errorf("after exhaustion, status=%q, want failed", got.Status)
	}
}

func TestReapStale(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w")
	_ = created

	// Move heartbeat into the past.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET last_heartbeat_at = now() - interval '5 minutes' WHERE id=$1`, claimed.ID); err != nil {
		t.Fatal(err)
	}

	outcome, err := s.ReapStale(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if outcome.Requeued != 1 || outcome.Failed != 0 {
		t.Errorf("outcome=%+v, want Requeued=1 Failed=0", outcome)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusPending {
		t.Errorf("status=%q, want pending after reap", got.Status)
	}
	if got.LastError == nil || *got.LastError == "" {
		t.Errorf("last_error not set by reaper")
	}
}

func TestReapStale_ExhaustedGoesToFailed(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 1})
	claimed, _ := s.ClaimTask(ctx, "w") // attempt_count = 1
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET last_heartbeat_at = now() - interval '1 hour' WHERE id=$1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReapStale(ctx, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusFailed {
		t.Errorf("status=%q, want failed", got.Status)
	}
}

func TestReportResult_SuccessWrongOwnerFailsPrecondition(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	_, err := s.ReportResult(ctx, "w2", claimed.ID, true, "")
	if err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusClaimed {
		t.Errorf("status=%q, want claimed", got.Status)
	}
	if got.CompletedAt != nil {
		t.Errorf("completed_at=%v, want nil", got.CompletedAt)
	}
}

func TestReportResult_FailureWrongOwnerFailsPrecondition(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	_, err := s.ReportResult(ctx, "w2", claimed.ID, false, "bad")
	if err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusClaimed {
		t.Errorf("status=%q, want claimed", got.Status)
	}
	if got.LastError != nil {
		t.Errorf("last_error=%v, want nil", got.LastError)
	}
	if got.ScheduledFor != nil {
		t.Errorf("scheduled_for=%v, want nil", got.ScheduledFor)
	}
}
