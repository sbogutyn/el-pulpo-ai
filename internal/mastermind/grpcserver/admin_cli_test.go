package grpcserver

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func TestCancelTask_PendingHappy(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "cancel-me"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := admin.CancelTask(adminCtx(), &pb.CancelTaskRequest{Id: created.GetTask().GetId()}); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	_, err = admin.GetTask(adminCtx(), &pb.GetTaskRequest{Id: created.GetTask().GetId()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetTask after cancel: code=%v, want NotFound", status.Code(err))
	}
}

func TestCancelTask_BadUUID(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.CancelTask(adminCtx(), &pb.CancelTaskRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestCancelTask_NotFound(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.CancelTask(adminCtx(), &pb.CancelTaskRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestCancelTask_RefusesClaimed(t *testing.T) {
	admin, tasks, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "w1"}); err != nil {
		t.Fatal(err)
	}
	_, err = admin.CancelTask(adminCtx(), &pb.CancelTaskRequest{Id: created.GetTask().GetId()})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestRetryTask_CompletedHappy(t *testing.T) {
	admin, tasks, _ := startAdminBufServer(t)
	if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	claim, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ReportResult(workerCtx(), &pb.ReportResultRequest{
		WorkerId: "w1",
		TaskId:   claim.GetTask().GetId(),
		Outcome:  &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	resp, err := admin.RetryTask(adminCtx(), &pb.RetryTaskRequest{Id: claim.GetTask().GetId()})
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	if resp.GetTask().GetStatus() != "pending" {
		t.Errorf("status=%q, want pending", resp.GetTask().GetStatus())
	}
	if resp.GetTask().GetAttemptCount() != 0 {
		t.Errorf("attempt_count=%d, want 0", resp.GetTask().GetAttemptCount())
	}
	if resp.GetTask().GetClaimedBy() != "" {
		t.Errorf("claimed_by=%q, want cleared", resp.GetTask().GetClaimedBy())
	}
}

func TestRetryTask_RefusesClaimed(t *testing.T) {
	admin, tasks, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "w1"}); err != nil {
		t.Fatal(err)
	}
	_, err = admin.RetryTask(adminCtx(), &pb.RetryTaskRequest{Id: created.GetTask().GetId()})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestRetryTask_NotFound(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.RetryTask(adminCtx(), &pb.RetryTaskRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestListWorkers_Aggregates(t *testing.T) {
	admin, tasks, _ := startAdminBufServer(t)
	// Seed two tasks, claimed by two different workers.
	if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "worker-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "worker-b"}); err != nil {
		t.Fatal(err)
	}
	resp, err := admin.ListWorkers(adminCtx(), &pb.ListWorkersRequest{})
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	if len(resp.GetItems()) != 2 {
		t.Fatalf("items=%d, want 2: %+v", len(resp.GetItems()), resp.GetItems())
	}
	byID := map[string]*pb.WorkerInfo{}
	for _, it := range resp.GetItems() {
		byID[it.GetId()] = it
	}
	for _, id := range []string{"worker-a", "worker-b"} {
		w, ok := byID[id]
		if !ok {
			t.Errorf("missing worker %q in response", id)
			continue
		}
		if w.GetActiveTasks() != 1 {
			t.Errorf("%s.active=%d, want 1", id, w.GetActiveTasks())
		}
	}
}

func TestListWorkers_Empty(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	resp, err := admin.ListWorkers(adminCtx(), &pb.ListWorkersRequest{})
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	if len(resp.GetItems()) != 0 {
		t.Errorf("items=%d, want 0", len(resp.GetItems()))
	}
}
