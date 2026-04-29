//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// claimWithName creates a task with a priority above any previously-created
// task, then claims tasks until it sees the one with the given name. Stray
// tasks are completed so they don't clog the queue. This keeps tests
// resilient against the shared-DB suite, where earlier tests may leave
// `pending` rows behind.
func claimWithName(t *testing.T, worker pb.TaskServiceClient, admin pb.AdminServiceClient, workerID, name string) (id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Boost priority so our task is claimed before other pending tasks.
	// claimTask orders by priority DESC, then created_at ASC.
	if _, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name: name, Priority: 1000, Payload: instructionsPayload(nil),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	const maxAttempts = 50
	for attempts := 0; attempts < maxAttempts; attempts++ {
		resp, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: workerID})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		got := resp.GetTask()
		if got.GetName() == name {
			return got.GetId()
		}
		// Hand the stray task back via Complete so it doesn't clog the queue.
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: workerID, TaskId: got.GetId(),
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
	}
	t.Fatalf("claimWithName: never saw task %q after %d attempts", name, maxAttempts)
	return ""
}

func TestWorkerGRPC_ClaimEmpty(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)

	// Drain the queue first so this test is deterministic: claim up to 20
	// tasks, complete each one, then confirm the next claim returns NotFound.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wid := "e2e-drain-" + shortID()
	for i := 0; i < 20; i++ {
		resp, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: wid})
		if status.Code(err) == codes.NotFound {
			return
		}
		if err != nil {
			t.Fatalf("drain claim: %v", err)
		}
		if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: wid, TaskId: resp.GetTask().GetId(),
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		}); err != nil {
			t.Fatalf("drain complete: %v", err)
		}
	}
	// One more to confirm NotFound after the drain.
	_, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: wid})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("after drain: code=%s want NotFound", status.Code(err))
	}
}

func TestWorkerGRPC_ClaimReturnsTask(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-claim-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-claim-"+shortID())

	// Cleanup via Complete.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestWorkerGRPC_Heartbeat(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-hb-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-hb-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: wid, TaskId: id}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

func TestWorkerGRPC_HeartbeatForeign(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	// A claims; B heartbeats.
	wa := "e2e-hb-a-" + shortID()
	wb := "e2e-hb-b-" + shortID()
	id := claimWithName(t, worker, admin, wa, "worker-grpc-hb-foreign-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: wb, TaskId: id})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%s want FailedPrecondition", status.Code(err))
	}

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wa, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

func TestWorkerGRPC_UpdateProgress(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-up-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-up-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := worker.UpdateProgress(ctx, &pb.UpdateProgressRequest{
		WorkerId: wid, TaskId: id, Note: "half done",
	}); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	// No admin RPC for progress_note; we verify via the admin UI
	// separately (TestHTTP_TaskDetail covers that path).

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

func TestWorkerGRPC_AppendLog(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-log-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-log-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := worker.AppendLog(ctx, &pb.AppendLogRequest{
		WorkerId: wid, TaskId: id, Message: "hello",
	})
	if err != nil {
		t.Fatalf("append log: %v", err)
	}
	if resp.GetId() == 0 {
		t.Fatalf("append log returned id=0")
	}

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

func TestWorkerGRPC_ReportSuccess(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-succ-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-succ-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatalf("report success: %v", err)
	}
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "completed" {
		t.Errorf("status=%s want completed", got.GetTask().GetStatus())
	}
}

func TestWorkerGRPC_ReportRetry(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "worker-grpc-retry-" + shortID()
	// max_attempts=3 so first failure retries.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name: name, MaxAttempts: 3, Payload: instructionsPayload(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	wid := "e2e-retry-" + shortID()
	// Claim until we get the right one, to tolerate other pending tasks.
	var claimedID string
	for i := 0; i < 20; i++ {
		resp, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: wid})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if resp.GetTask().GetId() == id {
			claimedID = id
			break
		}
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: wid, TaskId: resp.GetTask().GetId(),
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
	}
	if claimedID == "" {
		t.Fatalf("never claimed %s", id)
	}

	// First failure — should not be terminal.
	_, err = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Failure_{Failure: &pb.ReportResultRequest_Failure{Message: "try 1"}},
	})
	if err != nil {
		t.Fatalf("report fail 1: %v", err)
	}
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "pending" {
		t.Errorf("after fail 1: status=%s want pending", got.GetTask().GetStatus())
	}
	if got.GetTask().GetLastError() != "try 1" {
		t.Errorf("last_error=%q want %q", got.GetTask().GetLastError(), "try 1")
	}

	// Requeue puts scheduled_for ~attempt_count*30s in the future; for the
	// suite we don't need to wait for retry eligibility, just verify the
	// retry state. Clean up by deleting via a future Journey test; here,
	// just bump attempts via admin to mark it terminal so later tests see
	// a stable state. We do that by claiming after the backoff — skip.
	// The task is left in pending with scheduled_for in the future; this
	// is fine because no other test targets this specific task.
}

func TestWorkerGRPC_ReportTerminal(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// max_attempts=1 so one failure is terminal.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:    "worker-grpc-terminal-" + shortID(),
		MaxAttempts: 1,
		Payload: instructionsPayload(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	wid := "e2e-term-" + shortID()
	var claimed bool
	for i := 0; i < 20; i++ {
		resp, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: wid})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if resp.GetTask().GetId() == id {
			claimed = true
			break
		}
		// Defer the stray back.
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: wid, TaskId: resp.GetTask().GetId(),
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
	}
	if !claimed {
		t.Fatalf("never claimed %s", id)
	}

	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Failure_{Failure: &pb.ReportResultRequest_Failure{Message: "boom"}},
	}); err != nil {
		t.Fatalf("report fail terminal: %v", err)
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "failed" {
		t.Errorf("status=%s want failed", got.GetTask().GetStatus())
	}
	if got.GetTask().GetLastError() != "boom" {
		t.Errorf("last_error=%q want %q", got.GetTask().GetLastError(), "boom")
	}
}

// TestWorkerGRPC_HeartbeatPromotesToInProgress covers the master-side
// rename from `running` to `in_progress`: the first heartbeat on a
// claimed task must flip its status string to "in_progress" (the
// migration renamed the enum value, so a test that hard-codes the
// string protects the API contract from a silent rename in either
// direction).
func TestWorkerGRPC_HeartbeatPromotesToInProgress(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-progress-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-progress-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: wid, TaskId: id}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "in_progress" {
		t.Errorf("status=%q want in_progress", got.GetTask().GetStatus())
	}

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

// TestWorkerGRPC_SetJiraURL exercises the new TaskService.SetJiraURL RPC.
// SetJiraURL is allowed any time the worker holds the claim (claimed or
// in_progress), and surfaces on the task as `jira_url`.
func TestWorkerGRPC_SetJiraURL(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-jira-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-jira-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const url = "https://acme.atlassian.net/browse/E2E-1"
	if _, err := worker.SetJiraURL(ctx, &pb.SetJiraURLRequest{
		WorkerId: wid, TaskId: id, Url: url,
	}); err != nil {
		t.Fatalf("SetJiraURL: %v", err)
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetJiraUrl() != url {
		t.Errorf("jira_url=%q want %q", got.GetTask().GetJiraUrl(), url)
	}

	// Foreign worker cannot set the URL.
	_, err = worker.SetJiraURL(ctx, &pb.SetJiraURLRequest{
		WorkerId: "someone-else", TaskId: id, Url: url,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("foreign SetJiraURL: code=%s want FailedPrecondition", status.Code(err))
	}

	// Cleanup: complete via the original worker.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

// TestWorkerGRPC_OpenPR drives a task through the new parked-PR flow:
// claim → heartbeat (claimed → in_progress) → OpenPR → admin sees
// status=pr_opened, github_pr_url set, claim released. The reaper must
// then *not* requeue the task even though the heartbeat is stale.
func TestWorkerGRPC_OpenPR(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-openpr-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-openpr-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Heartbeat once to flip claimed → in_progress: OpenPR is only
	// allowed from in_progress per the transition allow-list.
	if _, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: wid, TaskId: id}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	const prURL = "https://github.com/org/repo/pull/777"
	if _, err := worker.OpenPR(ctx, &pb.OpenPRRequest{
		WorkerId: wid, TaskId: id, GithubPrUrl: prURL,
	}); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "pr_opened" {
		t.Errorf("status=%q want pr_opened", got.GetTask().GetStatus())
	}
	if got.GetTask().GetGithubPrUrl() != prURL {
		t.Errorf("github_pr_url=%q want %q", got.GetTask().GetGithubPrUrl(), prURL)
	}
	if got.GetTask().GetClaimedBy() != "" {
		t.Errorf("claim not released: claimed_by=%q", got.GetTask().GetClaimedBy())
	}

	// The worker is now idle: a second OpenPR with the same id must
	// fail because the worker no longer owns the claim.
	_, err = worker.OpenPR(ctx, &pb.OpenPRRequest{
		WorkerId: wid, TaskId: id, GithubPrUrl: prURL,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("second OpenPR: code=%s want FailedPrecondition", status.Code(err))
	}

	// Cleanup: the parked task is finalized via admin in
	// TestAdminGRPC_FinalizeTask_ tests; here we hand it off by
	// finalising it as a success so subsequent tests see a clean queue.
	if _, err := admin.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
		Id:      id,
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	}); err != nil {
		t.Fatalf("cleanup FinalizeTask: %v", err)
	}
}

// TestWorkerGRPC_OpenPR_RejectsEmptyURL covers the InvalidArgument case
// surfaced when github_pr_url is empty — the store rejects the call
// before any state mutation.
func TestWorkerGRPC_OpenPR_RejectsEmptyURL(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-openpr-empty-" + shortID()
	id := claimWithName(t, worker, admin, wid, "worker-grpc-openpr-empty-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Heartbeat to flip into in_progress.
	if _, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: wid, TaskId: id}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	_, err := worker.OpenPR(ctx, &pb.OpenPRRequest{WorkerId: wid, TaskId: id, GithubPrUrl: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty github_pr_url: code=%s want InvalidArgument", status.Code(err))
	}

	// Cleanup.
	_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	})
}

func TestAuthMatrix_GRPC(t *testing.T) {
	requireEndpointsReady(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Worker token on admin method.
	badAdmin := pb.NewAdminServiceClient(dialGRPC(t, S.WorkerToken))
	_, err := badAdmin.ListTasks(ctx, &pb.ListTasksRequest{Limit: 1})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("worker->admin: code=%s want Unauthenticated", status.Code(err))
	}

	// Admin token on worker method.
	badWorker := pb.NewTaskServiceClient(dialGRPC(t, S.AdminToken))
	_, err = badWorker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "bad"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("admin->worker: code=%s want Unauthenticated", status.Code(err))
	}

	// Wrong token on any method.
	wrong := pb.NewAdminServiceClient(dialGRPC(t, "completely-wrong"))
	_, err = wrong.ListTasks(ctx, &pb.ListTasksRequest{Limit: 1})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("wrong->admin: code=%s want Unauthenticated", status.Code(err))
	}
}
