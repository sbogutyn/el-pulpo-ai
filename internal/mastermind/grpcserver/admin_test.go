package grpcserver

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

const (
	testWorkerToken = "worker-tok"
	testAdminToken  = "admin-tok"
)

// startAdminBufServer stands up the full gRPC server (both services) with the
// per-method auth policy that main.go will use in production.
func startAdminBufServer(t *testing.T) (pb.AdminServiceClient, pb.TaskServiceClient, *store.Store) {
	t.Helper()
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatal(err)
	}

	policy := map[string]string{
		"/elpulpo.tasks.v1.TaskService/ClaimTask":      testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":      testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult":   testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/UpdateProgress": testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/AppendLog":      testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/SetJiraURL":     testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/OpenPR":         testWorkerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":    testAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":       testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":     testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTaskLogs":  testAdminToken,
		"/elpulpo.tasks.v1.AdminService/CancelTask":    testAdminToken,
		"/elpulpo.tasks.v1.AdminService/RetryTask":     testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListWorkers":   testAdminToken,
		"/elpulpo.tasks.v1.AdminService/RequestReview": testAdminToken,
		"/elpulpo.tasks.v1.AdminService/FinalizeTask":  testAdminToken,
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterTaskServiceServer(srv, New(s))
	pb.RegisterAdminServiceServer(srv, NewAdmin(s))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return pb.NewAdminServiceClient(conn), pb.NewTaskServiceClient(conn), s
}

// adminCtx attaches the admin bearer token.
func adminCtx() context.Context {
	return metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+testAdminToken))
}

// workerCtx attaches the worker bearer token.
func workerCtx() context.Context {
	return metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+testWorkerToken))
}

func TestCreateTask_Happy(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	resp, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{
		Name:        "indexer-run",
		Payload:     []byte(`{"instructions":"index the repo","k":"v"}`),
		Priority:    5,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.Task.Name != "indexer-run" {
		t.Errorf("Name=%q, want %q", resp.Task.Name, "indexer-run")
	}
	if resp.Task.Priority != 5 {
		t.Errorf("Priority=%d, want 5", resp.Task.Priority)
	}
	if resp.Task.MaxAttempts != 2 {
		t.Errorf("MaxAttempts=%d, want 2", resp.Task.MaxAttempts)
	}
	if resp.Task.Status != "pending" {
		t.Errorf("Status=%q, want pending", resp.Task.Status)
	}
	if resp.Task.Id == "" {
		t.Error("Id is empty")
	}
}

func TestCreateTask_MissingName(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestCreateTask_BadJSONPayload(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{
		Name:    "x",
		Payload: []byte("{not json"),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestCreateTask_RejectsWorkerToken(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.CreateTask(workerCtx(), &pb.CreateTaskRequest{Name: "x"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code=%v, want Unauthenticated", status.Code(err))
	}
}

func TestCreateTask_DefaultsMaxAttempts(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	resp, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "x", Payload: []byte(`{"instructions":"test"}`)})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.Task.MaxAttempts != 3 {
		t.Errorf("MaxAttempts=%d, want 3", resp.Task.MaxAttempts)
	}
}

func TestCreateTask_IssueRefsRoundTrip(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	resp, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{
		Name:        "with-refs",
		Payload:     []byte(`{"instructions":"test"}`),
		JiraUrl:     "https://pulpo.atlassian.net/browse/PULPO-1",
		GithubPrUrl: "https://github.com/sbogutyn/el-pulpo-ai/pull/1",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.Task.JiraUrl != "https://pulpo.atlassian.net/browse/PULPO-1" {
		t.Errorf("JiraUrl=%q, want full URL", resp.Task.JiraUrl)
	}
	if resp.Task.GithubPrUrl != "https://github.com/sbogutyn/el-pulpo-ai/pull/1" {
		t.Errorf("GithubPrUrl=%q, want full URL", resp.Task.GithubPrUrl)
	}
}

func TestGetTask_Happy(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t", Payload: []byte(`{"instructions":"test"}`)})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := admin.GetTask(adminCtx(), &pb.GetTaskRequest{Id: created.Task.Id})
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Task.Id != created.Task.Id {
		t.Errorf("Id mismatch: got %q, want %q", got.Task.Id, created.Task.Id)
	}
}

func TestGetTask_BadUUID(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.GetTask(adminCtx(), &pb.GetTaskRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestGetTask_NotFound(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	// Valid but unknown UUID.
	_, err := admin.GetTask(adminCtx(), &pb.GetTaskRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestListTasks_EmptyFilter(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	for i := 0; i < 3; i++ {
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t", Payload: []byte(`{"instructions":"test"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := admin.ListTasks(adminCtx(), &pb.ListTasksRequest{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("Total=%d, want 3", resp.Total)
	}
	if len(resp.Items) != 3 {
		t.Errorf("len(Items)=%d, want 3", len(resp.Items))
	}
}

func TestListTasks_StatusFilter(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	for i := 0; i < 2; i++ {
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t", Payload: []byte(`{"instructions":"test"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := admin.ListTasks(adminCtx(), &pb.ListTasksRequest{Status: "pending"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("Total=%d, want 2", resp.Total)
	}
	resp, err = admin.ListTasks(adminCtx(), &pb.ListTasksRequest{Status: "completed"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("Total=%d, want 0", resp.Total)
	}
}

func TestListTasks_BadStatus(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.ListTasks(adminCtx(), &pb.ListTasksRequest{Status: "nope"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestListTaskLogs_Happy(t *testing.T) {
	admin, tasks, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t", Payload: []byte(`{"instructions":"test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := tasks.ClaimTask(workerCtx(), &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"a", "b", "c"} {
		if _, err := tasks.AppendLog(workerCtx(), &pb.AppendLogRequest{
			WorkerId: "w1", TaskId: claim.GetTask().GetId(), Message: msg,
		}); err != nil {
			t.Fatalf("AppendLog %q: %v", msg, err)
		}
	}
	resp, err := admin.ListTaskLogs(adminCtx(), &pb.ListTaskLogsRequest{Id: created.Task.Id})
	if err != nil {
		t.Fatalf("ListTaskLogs: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items=%d, want 3", len(resp.Items))
	}
	if resp.Items[0].Message != "a" || resp.Items[2].Message != "c" {
		t.Errorf("ordering wrong: %v", resp.Items)
	}
}

func TestListTaskLogs_UnknownTask(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.ListTaskLogs(adminCtx(), &pb.ListTaskLogsRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestListTasks_Pagination(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	for i := 0; i < 5; i++ {
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t", Payload: []byte(`{"instructions":"test"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := admin.ListTasks(adminCtx(), &pb.ListTasksRequest{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("Total=%d, want 5", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Errorf("len(Items)=%d, want 2", len(resp.Items))
	}
}

func TestCreateTask_RequiresInstructions(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(ctx, testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatal(err)
	}

	a := NewAdmin(s)

	cases := []struct {
		name    string
		payload []byte
	}{
		{"missing key", []byte(`{}`)},
		{"empty string", []byte(`{"instructions":""}`)},
		{"whitespace", []byte(`{"instructions":"   "}`)},
		{"wrong type", []byte(`{"instructions":42}`)},
		{"not json", []byte(`not-json`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.CreateTask(ctx, &pb.CreateTaskRequest{
				Name:    "t",
				Payload: tc.payload,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("got %v, want InvalidArgument", err)
			}
		})
	}
}

func TestCreateTask_AcceptsValidInstructions(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(ctx, testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatal(err)
	}

	a := NewAdmin(s)
	resp, err := a.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:    "t",
		Payload: []byte(`{"instructions":"do the thing"}`),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.GetTask().GetId() == "" {
		t.Error("missing id")
	}
}

// driveToPROpened creates a task and drives it through claim → heartbeat →
// open_pr, returning the task id as a string. Used by the parked-state
// finalize/review tests below.
func driveToPROpened(t *testing.T, admin pb.AdminServiceClient, worker pb.TaskServiceClient) string {
	t.Helper()
	ctx := adminCtx()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:    "t",
		Payload: []byte(`{"instructions":"go"}`),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	id := created.GetTask().GetId()
	wctx := workerCtx()
	if _, err := worker.ClaimTask(wctx, &pb.ClaimTaskRequest{WorkerId: "w1"}); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if _, err := worker.Heartbeat(wctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: id}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := worker.OpenPR(wctx, &pb.OpenPRRequest{
		WorkerId: "w1", TaskId: id, GithubPrUrl: "https://github.com/o/r/pull/1",
	}); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	return id
}

func TestRequestReview_HappyPath(t *testing.T) {
	admin, worker, _ := startAdminBufServer(t)
	id := driveToPROpened(t, admin, worker)

	resp, err := admin.RequestReview(adminCtx(), &pb.RequestReviewRequest{Id: id})
	if err != nil {
		t.Fatalf("RequestReview: %v", err)
	}
	if got := resp.GetTask().GetStatus(); got != "review_requested" {
		t.Errorf("status=%q, want review_requested", got)
	}
}

func TestRequestReview_RejectsFromInProgress(t *testing.T) {
	admin, worker, _ := startAdminBufServer(t)
	created, _ := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{
		Name:    "t",
		Payload: []byte(`{"instructions":"go"}`),
	})
	wctx := workerCtx()
	_, _ = worker.ClaimTask(wctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	_, _ = worker.Heartbeat(wctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})

	_, err := admin.RequestReview(adminCtx(), &pb.RequestReviewRequest{Id: created.GetTask().GetId()})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestRequestReview_BadUUID(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.RequestReview(adminCtx(), &pb.RequestReviewRequest{Id: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestRequestReview_NotFound(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.RequestReview(adminCtx(), &pb.RequestReviewRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestFinalizeTask_Success(t *testing.T) {
	admin, worker, _ := startAdminBufServer(t)
	id := driveToPROpened(t, admin, worker)

	resp, err := admin.FinalizeTask(adminCtx(), &pb.FinalizeTaskRequest{
		Id:      id,
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	})
	if err != nil {
		t.Fatalf("FinalizeTask: %v", err)
	}
	if got := resp.GetTask().GetStatus(); got != "completed" {
		t.Errorf("status=%q, want completed", got)
	}
}

func TestFinalizeTask_Failure(t *testing.T) {
	admin, worker, _ := startAdminBufServer(t)
	id := driveToPROpened(t, admin, worker)

	resp, err := admin.FinalizeTask(adminCtx(), &pb.FinalizeTaskRequest{
		Id: id,
		Outcome: &pb.FinalizeTaskRequest_Failure_{
			Failure: &pb.FinalizeTaskRequest_Failure{Message: "rejected"},
		},
	})
	if err != nil {
		t.Fatalf("FinalizeTask: %v", err)
	}
	if got := resp.GetTask().GetStatus(); got != "failed" {
		t.Errorf("status=%q, want failed", got)
	}
	if got := resp.GetTask().GetLastError(); got != "rejected" {
		t.Errorf("last_error=%q, want rejected", got)
	}
}

func TestFinalizeTask_RequiresOutcome(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.FinalizeTask(adminCtx(), &pb.FinalizeTaskRequest{Id: "00000000-0000-0000-0000-000000000000"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestFinalizeTask_RejectsFromInProgress(t *testing.T) {
	admin, worker, _ := startAdminBufServer(t)
	created, _ := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{
		Name:    "t",
		Payload: []byte(`{"instructions":"go"}`),
	})
	wctx := workerCtx()
	_, _ = worker.ClaimTask(wctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	_, _ = worker.Heartbeat(wctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})

	_, err := admin.FinalizeTask(adminCtx(), &pb.FinalizeTaskRequest{
		Id:      created.GetTask().GetId(),
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestFinalizeTask_NotFound(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	_, err := admin.FinalizeTask(adminCtx(), &pb.FinalizeTaskRequest{
		Id:      "00000000-0000-0000-0000-000000000000",
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}
