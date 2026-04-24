package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

func TestState_ClaimCompleteFlow(t *testing.T) {
	fx := newWorkerFixture(t)
	ctx := context.Background()
	seedTask(t, fx, "job-A")

	task, err := fx.state.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if task.Name() != "job-A" {
		t.Errorf("name=%q, want job-A", task.Name())
	}

	if err := fx.state.Progress(ctx, task.ID(), "working"); err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if _, err := fx.state.AppendLog(ctx, task.ID(), "starting"); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if _, err := fx.state.AppendLog(ctx, task.ID(), "finished"); err != nil {
		t.Fatalf("AppendLog #2: %v", err)
	}
	if err := fx.state.Complete(ctx, task.ID()); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Task should be completed and logs should be persisted.
	id, _ := uuid.Parse(task.ID())
	got, err := fx.store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusCompleted {
		t.Errorf("status=%q, want completed", got.Status)
	}
	logs, _ := fx.store.ListTaskLogs(ctx, id, 0)
	if len(logs) != 2 {
		t.Errorf("len(logs)=%d, want 2", len(logs))
	}
}

func TestState_ClaimNext_EmptyQueueReturnsErrNoTask(t *testing.T) {
	fx := newWorkerFixture(t)
	_, err := fx.state.ClaimNext(context.Background())
	if !errors.Is(err, taskclient.ErrNoTask) {
		t.Errorf("err=%v, want ErrNoTask", err)
	}
}

func TestState_ClaimNext_AlreadyHoldingReturnsSentinel(t *testing.T) {
	fx := newWorkerFixture(t)
	ctx := context.Background()
	seedTask(t, fx, "job-A")
	seedTask(t, fx, "job-B")

	first, err := fx.state.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := fx.state.ClaimNext(ctx)
	if !errors.Is(err, ErrAlreadyHaveTask) {
		t.Errorf("err=%v, want ErrAlreadyHaveTask", err)
	}
	if again.ID() != first.ID() {
		t.Errorf("returned task id changed while holding")
	}
}

func TestState_FailReleasesClaim(t *testing.T) {
	fx := newWorkerFixture(t)
	ctx := context.Background()
	seedTask(t, fx, "job-A")

	t1, err := fx.state.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.state.Fail(ctx, t1.ID(), "boom"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Must now be idle.
	if _, err := fx.state.Current(); !errors.Is(err, ErrNoCurrentTask) {
		t.Errorf("Current err=%v, want ErrNoCurrentTask", err)
	}
}

func TestState_TaskIDMismatchRejected(t *testing.T) {
	fx := newWorkerFixture(t)
	ctx := context.Background()
	seedTask(t, fx, "job-A")
	if _, err := fx.state.ClaimNext(ctx); err != nil {
		t.Fatal(err)
	}
	err := fx.state.Progress(ctx, uuid.New().String(), "x")
	if !errors.Is(err, ErrTaskNotMatching) {
		t.Errorf("err=%v, want ErrTaskNotMatching", err)
	}
}

func seedTask(t *testing.T, fx *workerFixture, name string) uuid.UUID {
	t.Helper()
	created, err := fx.store.CreateTask(context.Background(), store.NewTaskInput{
		Name:        name,
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return created.ID
}
