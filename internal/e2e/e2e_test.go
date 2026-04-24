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
