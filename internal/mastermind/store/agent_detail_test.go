package store

import (
	"context"
	"errors"
	"testing"
)

func TestGetAgentDetail_NotFoundForUnknownWorker(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, err := s.GetAgentDetail(ctx, "ghost", 0, 0)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("got %v, want ErrAgentNotFound", err)
	}
}

func TestGetAgentDetail_AggregatesMetadata(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	// Worker has 3 historical tasks: completed, failed (terminal), and one
	// currently in flight.
	for range 3 {
		if _, err := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 1}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	t1, _ := s.ClaimTask(ctx, "orca-01")
	if _, err := s.ReportResult(ctx, "orca-01", t1.ID, true, ""); err != nil {
		t.Fatalf("complete t1: %v", err)
	}
	t2, _ := s.ClaimTask(ctx, "orca-01")
	if _, err := s.ReportResult(ctx, "orca-01", t2.ID, false, "boom"); err != nil {
		t.Fatalf("fail t2: %v", err)
	}
	t3, _ := s.ClaimTask(ctx, "orca-01")
	// Add a couple of log lines to the active task so the tail isn't empty.
	for _, msg := range []string{"hello", "world"} {
		if _, err := s.AppendTaskLog(ctx, "orca-01", t3.ID, msg); err != nil {
			t.Fatalf("AppendTaskLog: %v", err)
		}
	}

	d, err := s.GetAgentDetail(ctx, "orca-01", 5, 50)
	if err != nil {
		t.Fatalf("GetAgentDetail: %v", err)
	}

	if d.Info.ID != "orca-01" {
		t.Errorf("Info.ID=%q", d.Info.ID)
	}
	if d.Info.ActiveTasks != 1 {
		t.Errorf("ActiveTasks=%d, want 1", d.Info.ActiveTasks)
	}
	if d.Info.CompletedTasks != 1 {
		t.Errorf("CompletedTasks=%d, want 1", d.Info.CompletedTasks)
	}
	if d.Info.FailedTasks != 1 {
		t.Errorf("FailedTasks=%d, want 1", d.Info.FailedTasks)
	}
	if d.CurrentTask == nil || d.CurrentTask.ID != t3.ID {
		t.Errorf("CurrentTask mismatch: %+v", d.CurrentTask)
	}
	if len(d.RecentTasks) != 3 {
		t.Errorf("RecentTasks=%d, want 3", len(d.RecentTasks))
	}
	if len(d.Logs) != 2 {
		t.Fatalf("Logs=%d, want 2", len(d.Logs))
	}
	// Chronological order: oldest first.
	if d.Logs[0].Message != "hello" || d.Logs[1].Message != "world" {
		t.Errorf("logs not chronological: %+v", d.Logs)
	}
	if d.Logs[0].TaskName != "t" {
		t.Errorf("Logs[0].TaskName=%q, want 't'", d.Logs[0].TaskName)
	}
}

func TestGetAgentDetail_LogTailLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	if _, err := s.CreateTask(ctx, NewTaskInput{Name: "t"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, _ := s.ClaimTask(ctx, "finch-02")
	for i := 1; i <= 6; i++ {
		if _, err := s.AppendTaskLog(ctx, "finch-02", claimed.ID, "msg"); err != nil {
			t.Fatalf("AppendTaskLog: %v", err)
		}
	}

	d, err := s.GetAgentDetail(ctx, "finch-02", 0, 4)
	if err != nil {
		t.Fatalf("GetAgentDetail: %v", err)
	}
	if len(d.Logs) != 4 {
		t.Errorf("Logs=%d, want 4 (tail limit)", len(d.Logs))
	}
}
