package mcpserver

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
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
)

const testAdminToken = "admin-tok"

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
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
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

// startAdminBuf creates a fresh store, truncates tasks, and returns an
// AdminServiceClient connected to a bufconn-served AdminService guarded by
// the per-method auth policy.
func startAdminBuf(t *testing.T) (pb.AdminServiceClient, *store.Store) {
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
		"/elpulpo.tasks.v1.AdminService/CreateTask": testAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":    testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":  testAdminToken,
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterAdminServiceServer(srv, grpcserver.NewAdmin(s))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(testAdminToken)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return pb.NewAdminServiceClient(conn), s
}
