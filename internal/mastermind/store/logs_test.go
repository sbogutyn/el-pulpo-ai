package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAppendTaskLog_OwnerAppendsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	e1, err := s.AppendTaskLog(ctx, "w1", claimed.ID, "first")
	if err != nil {
		t.Fatalf("AppendTaskLog: %v", err)
	}
	if e1.Message != "first" {
		t.Errorf("message=%q, want first", e1.Message)
	}
	if _, err := s.AppendTaskLog(ctx, "w1", claimed.ID, "second"); err != nil {
		t.Fatalf("AppendTaskLog #2: %v", err)
	}

	// Appending should also refresh the lease (tasks.last_heartbeat_at) and
	// transition claimed -> running.
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusRunning {
		t.Errorf("status=%q, want running", got.Status)
	}

	logs, err := s.ListTaskLogs(ctx, claimed.ID, 0)
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("len(logs)=%d, want 2", len(logs))
	}
	if logs[0].Message != "first" || logs[1].Message != "second" {
		t.Errorf("unexpected order: %q, %q", logs[0].Message, logs[1].Message)
	}
}

func TestAppendTaskLog_WrongOwnerRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	if _, err := s.AppendTaskLog(ctx, "w2", claimed.ID, "nope"); err != ErrNotOwner {
		t.Errorf("err=%v, want ErrNotOwner", err)
	}
}

func TestAppendTaskLog_UnknownTaskReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	bogus := uuid.New()
	if _, err := s.AppendTaskLog(ctx, "w1", bogus, "x"); err != ErrNotFound {
		t.Errorf("err=%v, want ErrNotFound", err)
	}
}

func TestAppendTaskLog_AfterCompleteRejected(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	if _, err := s.ReportResult(ctx, "w1", claimed.ID, true, ""); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if _, err := s.AppendTaskLog(ctx, "w1", claimed.ID, "late"); err != ErrNotOwner {
		t.Errorf("err=%v, want ErrNotOwner (task already completed)", err)
	}
}
