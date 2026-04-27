package main

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/grpcserver"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

const smokeAdminToken = "admin-tok"

// TestSmoke_CLI_RoundTrip compiles the elpulpo binary, points it at a real
// mastermind gRPC server backed by a throwaway Postgres, and walks through
// the full create/get/list/retry/cancel/workers lifecycle. This guards
// against drift between the CLI's output and the admin RPCs (e.g. the CLI
// still working after a proto change).
func TestSmoke_CLI_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("pulpo"),
		postgres.WithUsername("pulpo"),
		postgres.WithPassword("pulpo"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("pg: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	mg, err := migrate.New("file://"+migrationsDir, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := mg.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	policy := map[string]string{
		"/elpulpo.tasks.v1.AdminService/CreateTask":  smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":     smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":   smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/CancelTask":  smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/RetryTask":   smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListWorkers": smokeAdminToken,
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterAdminServiceServer(srv, grpcserver.NewAdmin(st))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	bin := filepath.Join(t.TempDir(), "elpulpo")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = &bytes.Buffer{}
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v\n%s", err, build.Stderr.(*bytes.Buffer).String())
	}

	run := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(cmd.Environ(),
			"MASTERMIND_ADDR="+addr,
			"ADMIN_TOKEN="+smokeAdminToken,
			"DIAL_TIMEOUT=5s",
			"REQUEST_TIMEOUT=10s",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s %v: %v\nstderr: %s", bin, args, err, stderr.String())
		}
		return stdout.String()
	}

	// 1. create
	out := run(t, "tasks", "create", "--name", "indexer", "--payload", `{"instructions":"index the repo","k":"v"}`)
	if !strings.Contains(out, "indexer") {
		t.Fatalf("create output missing task name: %s", out)
	}

	// Extract the task ID from the human-readable output (ID\t<uuid>).
	var taskID string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "ID" {
			taskID = parts[1]
			break
		}
	}
	if taskID == "" {
		t.Fatalf("could not extract ID from output: %s", out)
	}

	// 2. get
	out = run(t, "tasks", "get", taskID)
	if !strings.Contains(out, "pending") {
		t.Errorf("get output missing status: %s", out)
	}

	// 3. list (JSON)
	out = run(t, "tasks", "list", "--json")
	if !strings.Contains(out, `"total": 1`) {
		t.Errorf("list output missing total=1: %s", out)
	}

	// 4. workers list (none yet — no worker has claimed)
	out = run(t, "workers", "list")
	if !strings.Contains(out, "no workers") {
		t.Errorf("workers list should report empty: %s", out)
	}

	// 5. retry (still pending — allowed)
	out = run(t, "tasks", "retry", taskID)
	if !strings.Contains(out, "requeued task "+taskID) {
		t.Errorf("retry output unexpected: %s", out)
	}

	// 6. cancel
	out = run(t, "tasks", "cancel", taskID)
	if !strings.Contains(out, "cancelled task "+taskID) {
		t.Errorf("cancel output unexpected: %s", out)
	}

	// 7. list now empty
	out = run(t, "tasks", "list")
	if !strings.Contains(out, "no tasks") {
		t.Errorf("post-cancel list unexpected: %s", out)
	}
}
