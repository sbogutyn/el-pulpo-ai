package cli

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// fakeAdmin is a stub AdminServiceServer that lets each test decide how the
// gRPC call should resolve. It purposefully does not touch Postgres: the
// CLI layer is under test here, not the admin service.
type fakeAdmin struct {
	pb.UnimplementedAdminServiceServer

	createFn        func(context.Context, *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error)
	getFn           func(context.Context, *pb.GetTaskRequest) (*pb.GetTaskResponse, error)
	listFn          func(context.Context, *pb.ListTasksRequest) (*pb.ListTasksResponse, error)
	cancelFn        func(context.Context, *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error)
	retryFn         func(context.Context, *pb.RetryTaskRequest) (*pb.RetryTaskResponse, error)
	listWorkersFn   func(context.Context, *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error)
	requestReviewFn func(context.Context, *pb.RequestReviewRequest) (*pb.RequestReviewResponse, error)
	finalizeTaskFn  func(context.Context, *pb.FinalizeTaskRequest) (*pb.FinalizeTaskResponse, error)
}

func (f *fakeAdmin) CreateTask(ctx context.Context, in *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	if f.createFn == nil {
		return nil, status.Error(codes.Unimplemented, "createFn not set")
	}
	return f.createFn(ctx, in)
}

func (f *fakeAdmin) GetTask(ctx context.Context, in *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	if f.getFn == nil {
		return nil, status.Error(codes.Unimplemented, "getFn not set")
	}
	return f.getFn(ctx, in)
}

func (f *fakeAdmin) ListTasks(ctx context.Context, in *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	if f.listFn == nil {
		return nil, status.Error(codes.Unimplemented, "listFn not set")
	}
	return f.listFn(ctx, in)
}

func (f *fakeAdmin) CancelTask(ctx context.Context, in *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	if f.cancelFn == nil {
		return nil, status.Error(codes.Unimplemented, "cancelFn not set")
	}
	return f.cancelFn(ctx, in)
}

func (f *fakeAdmin) RetryTask(ctx context.Context, in *pb.RetryTaskRequest) (*pb.RetryTaskResponse, error) {
	if f.retryFn == nil {
		return nil, status.Error(codes.Unimplemented, "retryFn not set")
	}
	return f.retryFn(ctx, in)
}

func (f *fakeAdmin) ListWorkers(ctx context.Context, in *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
	if f.listWorkersFn == nil {
		return nil, status.Error(codes.Unimplemented, "listWorkersFn not set")
	}
	return f.listWorkersFn(ctx, in)
}

func (f *fakeAdmin) RequestReview(ctx context.Context, in *pb.RequestReviewRequest) (*pb.RequestReviewResponse, error) {
	if f.requestReviewFn == nil {
		return nil, status.Error(codes.Unimplemented, "requestReviewFn not set")
	}
	return f.requestReviewFn(ctx, in)
}

func (f *fakeAdmin) FinalizeTask(ctx context.Context, in *pb.FinalizeTaskRequest) (*pb.FinalizeTaskResponse, error) {
	if f.finalizeTaskFn == nil {
		return nil, status.Error(codes.Unimplemented, "finalizeTaskFn not set")
	}
	return f.finalizeTaskFn(ctx, in)
}

const testAdminToken = "admin-tok"

// startFakeAdmin stands up a bufconn gRPC server with the same per-method
// bearer policy main.go uses. It sets MASTERMIND_ADDR / ADMIN_TOKEN / the
// internal dialer override so the CLI under test talks to this fake.
func startFakeAdmin(t *testing.T, f *fakeAdmin) {
	t.Helper()

	policy := map[string]string{
		"/elpulpo.tasks.v1.AdminService/CreateTask":  testAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":     testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":   testAdminToken,
		"/elpulpo.tasks.v1.AdminService/CancelTask":     testAdminToken,
		"/elpulpo.tasks.v1.AdminService/RetryTask":      testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListWorkers":    testAdminToken,
		"/elpulpo.tasks.v1.AdminService/RequestReview":  testAdminToken,
		"/elpulpo.tasks.v1.AdminService/FinalizeTask":   testAdminToken,
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterAdminServiceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	oldDialer := newClientConn
	newClientConn = func(_ context.Context, _ Config) (grpc.ClientConnInterface, func() error, error) {
		dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(dialer),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithPerRPCCredentials(auth.BearerCredentials(testAdminToken)))
		if err != nil {
			return nil, nil, err
		}
		return conn, conn.Close, nil
	}
	t.Cleanup(func() { newClientConn = oldDialer })

	// Also set env vars so LoadConfig accepts the empty-address path.
	t.Setenv("MASTERMIND_ADDR", "bufnet")
	t.Setenv("ADMIN_TOKEN", testAdminToken)
}

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Run(ctx, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestCLI_MissingCommand(t *testing.T) {
	_, _, err := runCLI(t)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestCLI_Help(t *testing.T) {
	// Help must not require environment config and must not fail.
	t.Setenv("MASTERMIND_ADDR", "")
	t.Setenv("ADMIN_TOKEN", "")
	stdout, _, err := runCLI(t, "help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stdout, "elpulpo") {
		t.Errorf("help output missing program name: %s", stdout)
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	_, _, err := runCLI(t, "nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCLI_TasksCreate_RequiresName(t *testing.T) {
	f := &fakeAdmin{}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "create")
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("err=%v, want --name is required", err)
	}
}

func TestCLI_TasksCreate_Happy(t *testing.T) {
	f := &fakeAdmin{
		createFn: func(_ context.Context, in *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
			if in.GetName() != "indexer-run" {
				t.Errorf("Name=%q, want indexer-run", in.GetName())
			}
			if string(in.GetPayload()) != `{"k":"v"}` {
				t.Errorf("Payload=%q, want {\"k\":\"v\"}", string(in.GetPayload()))
			}
			return &pb.CreateTaskResponse{Task: &pb.TaskDetail{
				Id: "00000000-0000-0000-0000-000000000001", Name: in.GetName(), Status: "pending",
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "create", "--name", "indexer-run", "--payload", `{"k":"v"}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "indexer-run") {
		t.Errorf("stdout does not contain task name: %s", stdout)
	}
}

func TestCLI_TasksCreate_RejectsInvalidJSON(t *testing.T) {
	f := &fakeAdmin{}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "create", "--name", "x", "--payload", "{not json")
	if err == nil {
		t.Fatal("expected JSON validation error")
	}
}

func TestCLI_TasksGet_Happy(t *testing.T) {
	f := &fakeAdmin{
		getFn: func(_ context.Context, in *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
			if in.GetId() != "abc" {
				t.Errorf("Id=%q, want abc", in.GetId())
			}
			return &pb.GetTaskResponse{Task: &pb.TaskDetail{Id: "abc", Name: "t", Status: "pending"}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "get", "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(stdout, "abc") || !strings.Contains(stdout, "pending") {
		t.Errorf("stdout=%s", stdout)
	}
}

func TestCLI_TasksList_JSON(t *testing.T) {
	f := &fakeAdmin{
		listFn: func(_ context.Context, in *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
			if in.GetStatus() != "pending" {
				t.Errorf("Status=%q, want pending", in.GetStatus())
			}
			return &pb.ListTasksResponse{Total: 1, Items: []*pb.TaskDetail{{
				Id: "id-1", Name: "t", Status: "pending",
			}}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "list", "--status", "pending", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, `"total": 1`) || !strings.Contains(stdout, `"id-1"`) {
		t.Errorf("stdout=%s", stdout)
	}
}

func TestCLI_TasksCancel_Happy(t *testing.T) {
	f := &fakeAdmin{
		cancelFn: func(_ context.Context, in *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
			if in.GetId() != "abc" {
				t.Errorf("Id=%q, want abc", in.GetId())
			}
			return &pb.CancelTaskResponse{}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "cancel", "abc")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(stdout, "cancelled task abc") {
		t.Errorf("stdout=%s", stdout)
	}
}

func TestCLI_TasksRetry_Happy(t *testing.T) {
	f := &fakeAdmin{
		retryFn: func(_ context.Context, in *pb.RetryTaskRequest) (*pb.RetryTaskResponse, error) {
			if in.GetId() != "abc" {
				t.Errorf("Id=%q, want abc", in.GetId())
			}
			return &pb.RetryTaskResponse{Task: &pb.TaskDetail{Id: "abc", Name: "t", Status: "pending"}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "retry", "abc")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !strings.Contains(stdout, "requeued task abc") {
		t.Errorf("stdout=%s", stdout)
	}
}

func TestCLI_TasksRetry_PropagatesErrors(t *testing.T) {
	f := &fakeAdmin{
		retryFn: func(_ context.Context, _ *pb.RetryTaskRequest) (*pb.RetryTaskResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "cannot retry an active task")
		},
	}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "retry", "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot retry") {
		t.Errorf("err=%v", err)
	}
}

func TestCLI_WorkersList_Table(t *testing.T) {
	f := &fakeAdmin{
		listWorkersFn: func(_ context.Context, _ *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
			return &pb.ListWorkersResponse{Items: []*pb.WorkerInfo{
				{Id: "worker-a", ActiveTasks: 1, CompletedTasks: 3, FailedTasks: 0},
				{Id: "worker-b", ActiveTasks: 0, CompletedTasks: 7, FailedTasks: 1},
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "workers", "list")
	if err != nil {
		t.Fatalf("workers list: %v", err)
	}
	for _, want := range []string{"worker-a", "worker-b", "ACTIVE", "COMPLETED"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q: %s", want, stdout)
		}
	}
}

func TestCLI_WorkersList_EmptyJSON(t *testing.T) {
	f := &fakeAdmin{
		listWorkersFn: func(_ context.Context, _ *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
			return &pb.ListWorkersResponse{}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "workers", "list", "--json")
	if err != nil {
		t.Fatalf("workers list: %v", err)
	}
	if !strings.Contains(stdout, `"items": []`) && !strings.Contains(stdout, `"items": null`) {
		t.Errorf("stdout=%s", stdout)
	}
}

func TestCLI_MissingAddr(t *testing.T) {
	// Don't use fake dialer — force LoadConfig path.
	t.Setenv("MASTERMIND_ADDR", "")
	t.Setenv("ADMIN_TOKEN", "")
	_, _, err := runCLI(t, "tasks", "list")
	if err == nil {
		t.Fatal("expected error for missing MASTERMIND_ADDR")
	}
}

func TestCLI_TasksRequestReview_Happy(t *testing.T) {
	called := false
	f := &fakeAdmin{
		requestReviewFn: func(_ context.Context, in *pb.RequestReviewRequest) (*pb.RequestReviewResponse, error) {
			called = true
			if in.GetId() != "00000000-0000-0000-0000-000000000001" {
				t.Errorf("Id=%q, want fixed UUID", in.GetId())
			}
			return &pb.RequestReviewResponse{Task: &pb.TaskDetail{
				Id: in.GetId(), Name: "n", Status: "review_requested",
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "request-review", "00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("request-review: %v", err)
	}
	if !called {
		t.Fatal("RequestReview not called")
	}
	if !strings.Contains(stdout, "review_requested") {
		t.Errorf("stdout does not show review_requested status: %s", stdout)
	}
}

func TestCLI_TasksRequestReview_RequiresID(t *testing.T) {
	f := &fakeAdmin{}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "request-review")
	if err == nil {
		t.Fatal("expected error when id is missing")
	}
}

func TestCLI_TasksFinalize_Success(t *testing.T) {
	called := false
	f := &fakeAdmin{
		finalizeTaskFn: func(_ context.Context, in *pb.FinalizeTaskRequest) (*pb.FinalizeTaskResponse, error) {
			called = true
			switch in.GetOutcome().(type) {
			case *pb.FinalizeTaskRequest_Success_:
				// ok
			default:
				t.Errorf("expected Success outcome, got %T", in.GetOutcome())
			}
			return &pb.FinalizeTaskResponse{Task: &pb.TaskDetail{
				Id: in.GetId(), Name: "n", Status: "completed",
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "finalize", "00000000-0000-0000-0000-000000000001", "--success")
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !called {
		t.Fatal("FinalizeTask not called")
	}
	if !strings.Contains(stdout, "completed") {
		t.Errorf("stdout: %q", stdout)
	}
}

func TestCLI_TasksFinalize_Failure(t *testing.T) {
	f := &fakeAdmin{
		finalizeTaskFn: func(_ context.Context, in *pb.FinalizeTaskRequest) (*pb.FinalizeTaskResponse, error) {
			fail, ok := in.GetOutcome().(*pb.FinalizeTaskRequest_Failure_)
			if !ok {
				t.Fatalf("expected Failure outcome, got %T", in.GetOutcome())
			}
			if fail.Failure.GetMessage() != "rejected" {
				t.Errorf("message=%q, want rejected", fail.Failure.GetMessage())
			}
			return &pb.FinalizeTaskResponse{Task: &pb.TaskDetail{
				Id: in.GetId(), Status: "failed", LastError: "rejected",
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	stdout, _, err := runCLI(t, "tasks", "finalize", "00000000-0000-0000-0000-000000000001", "--fail", "rejected")
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("stdout: %q", stdout)
	}
}

func TestCLI_TasksFinalize_RequiresExactlyOneOutcome(t *testing.T) {
	f := &fakeAdmin{}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "finalize", "00000000-0000-0000-0000-000000000001")
	if err == nil {
		t.Fatal("expected error: no outcome flag")
	}
	_, _, err = runCLI(t, "tasks", "finalize", "00000000-0000-0000-0000-000000000001", "--success", "--fail", "x")
	if err == nil {
		t.Fatal("expected error: both outcomes set")
	}
}

func TestCLI_TasksCreate_Instructions_BuildsPayload(t *testing.T) {
	var seen []byte
	f := &fakeAdmin{
		createFn: func(_ context.Context, in *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
			seen = in.GetPayload()
			return &pb.CreateTaskResponse{Task: &pb.TaskDetail{
				Id: "00000000-0000-0000-0000-000000000001", Name: in.GetName(),
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "create", "--name", "x", "--instructions", "do the thing")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(string(seen), `"instructions":"do the thing"`) {
		t.Errorf("payload=%q does not contain instructions", string(seen))
	}
}

func TestCLI_TasksCreate_Instructions_MergesIntoPayload(t *testing.T) {
	var seen []byte
	f := &fakeAdmin{
		createFn: func(_ context.Context, in *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
			seen = in.GetPayload()
			return &pb.CreateTaskResponse{Task: &pb.TaskDetail{
				Id: "00000000-0000-0000-0000-000000000001", Name: in.GetName(),
			}}, nil
		},
	}
	startFakeAdmin(t, f)
	_, _, err := runCLI(t, "tasks", "create", "--name", "x",
		"--instructions", "go",
		"--payload", `{"repo":"pulpo"}`,
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(string(seen), `"instructions":"go"`) {
		t.Errorf("payload=%q does not contain instructions", string(seen))
	}
	if !strings.Contains(string(seen), `"repo":"pulpo"`) {
		t.Errorf("payload=%q does not contain repo", string(seen))
	}
}
