package e2e

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/grpcserver"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/reaper"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/runner"
)

const workerToken = "tok"

func TestE2E_100TasksAreEachRunOnce(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _ = s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE")

	const N = 100
	for i := 0; i < N; i++ {
		if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
			t.Fatal(err)
		}
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.BearerInterceptor(workerToken)))
	pb.RegisterTaskServiceServer(srv, grpcserver.New(s))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	rp := reaper.New(s, 50*time.Millisecond, 500*time.Millisecond, log)
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go rp.Run(rctx)

	runCtx, runCancel := context.WithTimeout(ctx, 30*time.Second)
	defer runCancel()

	var wg sync.WaitGroup
	const workers = 10
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient("passthrough:///bufnet",
				grpc.WithContextDialer(dialer),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithPerRPCCredentials(auth.BearerCredentials(workerToken)))
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer conn.Close()

			r := runner.New(pb.NewTaskServiceClient(conn), runner.Config{
				WorkerID:          uuid.New().String(),
				PollInterval:      10 * time.Millisecond,
				HeartbeatInterval: 30 * time.Millisecond,
				WorkDuration:      10 * time.Millisecond,
			}, log)
			r.Run(runCtx)
		}()
	}

	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			page, _ := s.ListTasks(ctx, store.ListTasksFilter{Status: strPtr(store.StatusCompleted), Limit: 200})
			t.Fatalf("did not complete in time; completed=%d/%d", page.Total, N)
		default:
		}
		page, _ := s.ListTasks(ctx, store.ListTasksFilter{Status: strPtr(store.StatusCompleted), Limit: 1})
		if page.Total == N {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	runCancel()
	wg.Wait()
}

func strPtr(s store.TaskStatus) *store.TaskStatus { return &s }

func TestE2E_PRPipelineHappyPath(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatal(err)
	}

	// Per-method policy so the worker token gates TaskService calls and the
	// admin token gates AdminService calls. Mirrors what main.go installs.
	const adminToken = "admin-tok"
	policy := map[string]string{
		"/elpulpo.tasks.v1.TaskService/ClaimTask":       workerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":       workerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult":    workerToken,
		"/elpulpo.tasks.v1.TaskService/UpdateProgress":  workerToken,
		"/elpulpo.tasks.v1.TaskService/AppendLog":       workerToken,
		"/elpulpo.tasks.v1.TaskService/SetJiraURL":      workerToken,
		"/elpulpo.tasks.v1.TaskService/OpenPR":          workerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":     adminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":        adminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":      adminToken,
		"/elpulpo.tasks.v1.AdminService/RequestReview":  adminToken,
		"/elpulpo.tasks.v1.AdminService/FinalizeTask":   adminToken,
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterTaskServiceServer(srv, grpcserver.New(s))
	pb.RegisterAdminServiceServer(srv, grpcserver.NewAdmin(s))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }

	workerConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(workerToken)))
	if err != nil {
		t.Fatal(err)
	}
	defer workerConn.Close()

	adminConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(adminToken)))
	if err != nil {
		t.Fatal(err)
	}
	defer adminConn.Close()

	worker := pb.NewTaskServiceClient(workerConn)
	admin := pb.NewAdminServiceClient(adminConn)

	// Admin: create a task with the canonical instructions payload.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:    "feature",
		Payload: []byte(`{"instructions":"implement X"}`),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	id := created.GetTask().GetId()
	if created.GetTask().GetStatus() != "pending" {
		t.Fatalf("after create, status=%q, want pending", created.GetTask().GetStatus())
	}

	// Worker: claim, heartbeat (drives to in_progress), set jira, open PR.
	claim, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claim.GetTask().GetId() != id {
		t.Fatalf("claim id mismatch: got %q want %q", claim.GetTask().GetId(), id)
	}
	if _, err := worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: id}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := worker.SetJiraURL(ctx, &pb.SetJiraURLRequest{
		WorkerId: "w1", TaskId: id, Url: "https://jira/T-1",
	}); err != nil {
		t.Fatalf("SetJiraURL: %v", err)
	}
	if _, err := worker.OpenPR(ctx, &pb.OpenPRRequest{
		WorkerId: "w1", TaskId: id, GithubPrUrl: "https://github.com/o/r/pull/1",
	}); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}

	// After OpenPR the worker is freed; the queue should be empty.
	if claim2, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"}); err == nil {
		t.Fatalf("after OpenPR, ClaimTask got %v, want NotFound (queue empty)", claim2)
	}

	// Admin: drive the parked task to review_requested and then to completed.
	if _, err := admin.RequestReview(ctx, &pb.RequestReviewRequest{Id: id}); err != nil {
		t.Fatalf("RequestReview: %v", err)
	}
	if _, err := admin.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
		Id:      id,
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	}); err != nil {
		t.Fatalf("FinalizeTask: %v", err)
	}

	// Final state assertions.
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.GetTask().GetStatus() != "completed" {
		t.Errorf("final status=%q, want completed", got.GetTask().GetStatus())
	}
	if got.GetTask().GetJiraUrl() != "https://jira/T-1" {
		t.Errorf("jira_url=%q, want preserved", got.GetTask().GetJiraUrl())
	}
	if got.GetTask().GetGithubPrUrl() != "https://github.com/o/r/pull/1" {
		t.Errorf("github_pr_url=%q, want preserved", got.GetTask().GetGithubPrUrl())
	}
}
