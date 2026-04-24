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
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks"); err != nil {
		t.Fatal(err)
	}

	policy := map[string]string{
		"/elpulpo.tasks.v1.TaskService/ClaimTask":    testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":    testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult": testWorkerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":  testAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":     testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":   testAdminToken,
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
		Payload:     []byte(`{"k":"v"}`),
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
	resp, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "x"})
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
