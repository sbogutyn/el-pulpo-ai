package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
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

func TestSmoke_InitializeAndToolsList(t *testing.T) {
	// 1. Bring up Postgres and mastermind gRPC on a loopback port.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		"/elpulpo.tasks.v1.AdminService/CreateTask": smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":    smokeAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":  smokeAdminToken,
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterAdminServiceServer(srv, grpcserver.NewAdmin(st))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 2. Build the binary.
	bin := filepath.Join(t.TempDir(), "mastermind-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	// 3. Launch it and drive the MCP handshake.
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"MASTERMIND_ADDR="+addr,
		"ADMIN_TOKEN="+smokeAdminToken,
		"DIAL_TIMEOUT=5s",
		"LOG_LEVEL=error",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})

	// 4. Send initialize.
	send := func(payload string) {
		if _, err := io.WriteString(stdin, payload+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`)

	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	if !strings.Contains(line, `"id":1`) || !strings.Contains(line, `"result"`) {
		t.Fatalf("init response malformed: %s", line)
	}

	// Required notification after initialize.
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// 5. Ask for the tool list.
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	line, err = r.ReadString('\n')
	if err != nil {
		t.Fatalf("read tools/list: %v", err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, line)
	}
	names := map[string]bool{}
	for _, tt := range resp.Result.Tools {
		names[tt.Name] = true
	}
	for _, want := range []string{"create_task", "get_task", "list_tasks"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %+v", want, names)
		}
	}

	fmt.Fprintln(os.Stderr, "smoke ok")
}
