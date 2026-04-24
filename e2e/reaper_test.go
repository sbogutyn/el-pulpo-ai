//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// TestReaper_Requeue verifies that a task claimed without heartbeats is
// reclaimed by the mastermind reaper and returned to `pending`. Relies on
// docker-compose.e2e.yml setting VISIBILITY_TIMEOUT=5s and
// REAPER_INTERVAL=1s.
func TestReaper_Requeue(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	worker := workerClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name: "reaper-requeue-" + shortID(), MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	// Claim deterministically.
	wid := "e2e-reaper-" + shortID()
	if _, err := claimExactly(ctx, worker, wid, id); err != nil {
		t.Fatal(err)
	}

	// Do NOT heartbeat. Wait past VISIBILITY_TIMEOUT + one reaper tick.
	err = eventually(contextWithDeadline(ctx, 12*time.Second), 500*time.Millisecond, func() error {
		got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
		if err != nil {
			return err
		}
		if got.GetTask().GetStatus() != "pending" {
			return fmt.Errorf("status=%s want pending", got.GetTask().GetStatus())
		}
		return nil
	})
	if err != nil {
		failWithLogs(t, "reaper did not requeue task: %v", err)
	}
}

// TestReaper_Terminal verifies that an exhausted-attempt task whose lease
// expires is reaped into `failed`, not requeued.
func TestReaper_Terminal(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	worker := workerClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create with MaxAttempts=1 and claim. One claim increments
	// attempt_count to 1, which equals max_attempts, so the reaper will
	// transition us straight to `failed`.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name: "reaper-terminal-" + shortID(), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	wid := "e2e-reaper-term-" + shortID()
	if _, err := claimExactly(ctx, worker, wid, id); err != nil {
		t.Fatal(err)
	}

	// Wait past the visibility window.
	err = eventually(contextWithDeadline(ctx, 12*time.Second), 500*time.Millisecond, func() error {
		got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
		if err != nil {
			return err
		}
		if got.GetTask().GetStatus() != "failed" {
			return fmt.Errorf("status=%s want failed", got.GetTask().GetStatus())
		}
		return nil
	})
	if err != nil {
		failWithLogs(t, "reaper did not terminate task: %v", err)
	}
}

// claimExactly keeps claiming and completing stray tasks until the
// targeted id is claimed. Returns the id claimed (always == wanted) or an
// error when the deadline fires.
func claimExactly(ctx context.Context, worker pb.TaskServiceClient, workerID, wanted string) (string, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	for time.Now().Before(deadline) {
		resp, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: workerID})
		if err != nil {
			return "", fmt.Errorf("claim: %w", err)
		}
		got := resp.GetTask().GetId()
		if got == wanted {
			return got, nil
		}
		// Hand back whatever we got.
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: workerID, TaskId: got,
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
	}
	return "", fmt.Errorf("claimExactly: never saw %s before deadline", wanted)
}

// contextWithDeadline returns a child context that will fire whichever is
// earlier: the parent's deadline or `dur` from now. The child is cancelled
// on the parent's cancellation; the cancel func is leaked on purpose
// because the lifetimes inside `eventually` are bounded by the deadline
// anyway. Keeping the cancel func would force callers to plumb it through
// the `check` closure — not worth the ergonomic cost for a test helper.
func contextWithDeadline(parent context.Context, dur time.Duration) context.Context {
	ctx, cancel := context.WithDeadline(parent, time.Now().Add(dur))
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}
