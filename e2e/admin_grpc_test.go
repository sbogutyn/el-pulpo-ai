//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func TestAdminGRPC_CreateHappy(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:        "admin-happy-" + shortID(),
		Payload:     []byte(`{"k":"v"}`),
		Priority:    1,
		MaxAttempts: 2,
		ScheduledFor: timestamppb.New(time.Now().Add(-1 * time.Second)),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got := resp.GetTask()
	if got.GetId() == "" {
		t.Fatal("CreateTask returned no id")
	}
	if got.GetStatus() != "pending" {
		t.Errorf("status = %q, want pending", got.GetStatus())
	}
	if got.GetMaxAttempts() != 2 {
		t.Errorf("max_attempts = %d, want 2", got.GetMaxAttempts())
	}
	if got.GetPriority() != 1 {
		t.Errorf("priority = %d, want 1", got.GetPriority())
	}
	// Postgres JSONB normalizes whitespace; compare semantically.
	if !jsonEqual(t, got.GetPayload(), `{"k":"v"}`) {
		t.Errorf("payload = %q, want semantically %q", got.GetPayload(), `{"k":"v"}`)
	}
}

// jsonEqual reports whether two JSON documents are semantically equal,
// ignoring whitespace and key order. Fails the test on bad JSON.
func jsonEqual(t *testing.T, a []byte, b string) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("jsonEqual: lhs not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Fatalf("jsonEqual: rhs not JSON: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}

func TestAdminGRPC_CreateInvalidName(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", status.Code(err))
	}
}

func TestAdminGRPC_CreateInvalidPayload(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:    "bad-payload",
		Payload: []byte(`{not json`),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument (%v)", status.Code(err), err)
	}
}

func TestAdminGRPC_GetTask(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "get-task-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetId() != id {
		t.Errorf("id = %q, want %q", got.GetTask().GetId(), id)
	}

	// Unknown but well-formed UUID.
	_, err = admin.GetTask(ctx, &pb.GetTaskRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("unknown-id code = %s, want NotFound", status.Code(err))
	}

	// Malformed UUID.
	_, err = admin.GetTask(ctx, &pb.GetTaskRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad-id code = %s, want InvalidArgument", status.Code(err))
	}
}

func TestAdminGRPC_ListAll(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create 3 tasks so the count is predictable-enough: total >= 3.
	prefix := "list-all-" + shortID()
	for i := 0; i < 3; i++ {
		if _, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: prefix}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	got := countByName(resp.GetItems(), prefix)
	if got < 3 {
		t.Fatalf("listed %d tasks named %q, want >= 3", got, prefix)
	}
	if resp.GetTotal() < int32(got) {
		t.Fatalf("total=%d but items with name=%s >= %d", resp.GetTotal(), prefix, got)
	}
}

func TestAdminGRPC_ListFiltered(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Filtered=pending should include our fresh task.
	name := "list-filtered-" + shortID()
	if _, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	resp, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Status: "pending", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if countByName(resp.GetItems(), name) != 1 {
		t.Fatalf("expected exactly one pending item named %q", name)
	}
	// Filtered=completed should not.
	respC, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Status: "completed", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if countByName(respC.GetItems(), name) != 0 {
		t.Fatalf("should not find pending task in completed filter")
	}
}

func TestAdminGRPC_ListInvalidStatus(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Status: "banana"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", status.Code(err))
	}
}

func TestAdminGRPC_ListLogs(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	worker := workerClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create, claim, append two logs, then list.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "logs-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	claimed, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "e2e-admin-grpc"})
	if err != nil {
		t.Fatal(err)
	}
	claimedID := claimed.GetTask().GetId()

	if claimedID != id {
		// The reaper test or a concurrent run may have snuck a task in.
		// Be friendly: complete whatever we claimed so it doesn't pollute,
		// then retry once.
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: "e2e-admin-grpc", TaskId: claimedID,
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
		claimed, err = worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "e2e-admin-grpc"})
		if err != nil {
			t.Fatal(err)
		}
		claimedID = claimed.GetTask().GetId()
	}

	for _, msg := range []string{"line one", "line two"} {
		if _, err := worker.AppendLog(ctx, &pb.AppendLogRequest{
			WorkerId: "e2e-admin-grpc", TaskId: claimedID, Message: msg,
		}); err != nil {
			t.Fatal(err)
		}
	}

	logsResp, err := admin.ListTaskLogs(ctx, &pb.ListTaskLogsRequest{Id: claimedID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(logsResp.GetItems()) < 2 {
		t.Fatalf("expected >= 2 log lines, got %d", len(logsResp.GetItems()))
	}
	// Order by id ASC so 'line one' must come first among the last two.
	items := logsResp.GetItems()
	last2 := items[len(items)-2:]
	if !strings.Contains(last2[0].GetMessage(), "line one") || !strings.Contains(last2[1].GetMessage(), "line two") {
		t.Fatalf("logs out of order: got %q / %q", last2[0].GetMessage(), last2[1].GetMessage())
	}

	// Cleanup: complete the task so subsequent tests have a clean queue.
	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: "e2e-admin-grpc", TaskId: claimedID,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatal(err)
	}

	// Unknown task -> NotFound.
	_, err = admin.ListTaskLogs(ctx, &pb.ListTaskLogsRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("unknown id: code=%s want NotFound", status.Code(err))
	}
}

// countByName returns how many items have a given Name. Used to avoid
// fragile total-count assertions.
func countByName(items []*pb.TaskDetail, name string) int {
	var n int
	for _, it := range items {
		if it.GetName() == name {
			n++
		}
	}
	return n
}
