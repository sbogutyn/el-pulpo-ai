package mcpserver

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/grpcserver"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

const testWorkerToken = "worker-tok"

var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("pulpo"),
		postgres.WithUsername("pulpo"),
		postgres.WithPassword("pulpo"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		panic(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	testDSN = dsn

	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
	mg, err := migrate.New("file://"+migrationsDir, dsn)
	if err != nil {
		panic(err)
	}
	if err := mg.Up(); err != nil && err != migrate.ErrNoChange {
		panic(err)
	}
	code := m.Run()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

// workerFixture wires a fresh store, a bufconn-served TaskService with the
// worker auth policy, a taskclient bound to a random worker id, and a State.
type workerFixture struct {
	store    *store.Store
	client   *taskclient.Client
	state    *State
	workerID string

	srv    *grpc.Server
	wg     sync.WaitGroup
	closed bool
}

func newWorkerFixture(t *testing.T) *workerFixture {
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
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterTaskServiceServer(srv, grpcserver.New(s))

	fx := &workerFixture{store: s, srv: srv}
	fx.wg.Go(func() {
		_ = srv.Serve(lis)
	})
	t.Cleanup(func() {
		if !fx.closed {
			srv.Stop()
			fx.wg.Wait()
		}
	})

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(testWorkerToken)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	fx.workerID = uuid.New().String()
	fx.client = taskclient.NewClient(pb.NewTaskServiceClient(conn), fx.workerID)
	// Heartbeat generously so tests don't see spurious renewals.
	fx.state = New(fx.client, 5*time.Second, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(fx.state.Release)
	return fx
}
