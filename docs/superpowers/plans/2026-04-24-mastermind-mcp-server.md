# Mastermind MCP Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a new `mastermind-mcp` Go binary that runs as a stdio MCP server spawned by a coding agent, exposing `create_task`, `get_task`, and `list_tasks` tools backed by a new `AdminService` gRPC on mastermind.

**Architecture:** The mastermind gains an `AdminService` colocated with `TaskService` on the existing gRPC listener, guarded by a new `ADMIN_TOKEN` via a per-method bearer interceptor. A new stdio binary in `cmd/mastermind-mcp` and a reusable `internal/mcpserver` package build an MCP server whose tool handlers translate calls into `AdminService` RPCs and map gRPC errors to MCP tool errors. The MCP binary holds no DB credentials and no business logic.

**Tech Stack:** Go 1.25, `google.golang.org/grpc`, `google.golang.org/protobuf` (with `google.protobuf.Timestamp`), `github.com/modelcontextprotocol/go-sdk/mcp`, `github.com/kelseyhightower/envconfig`, `log/slog`, `testcontainers-go` + `bufconn` for tests.

**Spec:** `docs/superpowers/specs/2026-04-24-mastermind-mcp-server-design.md`

---

## File structure

Files to create:
- `cmd/mastermind-mcp/main.go`
- `internal/mcpserver/config.go`
- `internal/mcpserver/config_test.go`
- `internal/mcpserver/server.go`
- `internal/mcpserver/tools.go`
- `internal/mcpserver/tools_test.go`
- `internal/mcpserver/testmain_test.go`
- `internal/mastermind/grpcserver/admin.go`
- `internal/mastermind/grpcserver/admin_test.go`
- `Dockerfile.mcp`

Files to modify:
- `internal/proto/tasks.proto` — add `google/protobuf/timestamp.proto` import, `AdminService`, `TaskDetail`, and request/response messages.
- `internal/proto/tasks.pb.go` — regenerated.
- `internal/proto/tasks_grpc.pb.go` — regenerated.
- `internal/auth/grpc.go` — add `PerMethodInterceptor`; keep `BearerInterceptor` intact for existing tests.
- `internal/auth/grpc_test.go` — tests for the new interceptor.
- `internal/config/config.go` — add `AdminToken` to the `Mastermind` struct.
- `internal/config/config_test.go` — cover new required field.
- `cmd/mastermind/main.go` — build per-method policy map; register both services; pass the interceptor.
- `cmd/mastermind/main.go` — also wire admin token env var.
- `Makefile` — add `build-mcp`, `run-mcp`; include binary in default `build`.
- `README.md` — add a short `.mcp.json` integration snippet.
- `go.mod` / `go.sum` — new MCP SDK dependency.

Files NOT touched:
- `internal/worker/*` — workers are unaffected.
- `internal/mastermind/store/*` — `admin.go` uses existing store methods unchanged.
- `internal/mastermind/httpserver/*` — admin UI continues to work as today.
- `migrations/*` — no schema changes.

---

## Task 1: Add `ADMIN_TOKEN` to mastermind config (TDD)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

Add a required `AdminToken` field to `Mastermind` config. This must land before anything that references the policy map, but before we write any new runtime code that would fail without it, so we do it first.

- [ ] **Step 1: Read the existing config test file**

Run: read `internal/config/config_test.go` to see the pattern used for the existing required fields (`WorkerToken`, `AdminUser`, etc.).

- [ ] **Step 2: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadMastermind_AdminTokenRequired(t *testing.T) {
	// Set every other required field; leave ADMIN_TOKEN unset.
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WORKER_TOKEN", "w")
	t.Setenv("ADMIN_USER", "u")
	t.Setenv("ADMIN_PASSWORD", "p")
	t.Setenv("ADMIN_TOKEN", "")

	_, err := LoadMastermind()
	if err == nil {
		t.Fatal("want error for missing ADMIN_TOKEN, got nil")
	}
	if !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Errorf("error %q should mention ADMIN_TOKEN", err.Error())
	}
}

func TestLoadMastermind_AdminTokenPresent(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WORKER_TOKEN", "w")
	t.Setenv("ADMIN_USER", "u")
	t.Setenv("ADMIN_PASSWORD", "p")
	t.Setenv("ADMIN_TOKEN", "a")

	cfg, err := LoadMastermind()
	if err != nil {
		t.Fatalf("LoadMastermind: %v", err)
	}
	if cfg.AdminToken != "a" {
		t.Errorf("AdminToken=%q, want %q", cfg.AdminToken, "a")
	}
}
```

Add `"strings"` to the import block if it isn't already imported.

- [ ] **Step 3: Run the tests and see them fail**

Run: `go test ./internal/config/... -run AdminToken -v`
Expected: compile error on `cfg.AdminToken` and/or "ADMIN_TOKEN" fallthrough.

- [ ] **Step 4: Add the field and validation**

Edit `internal/config/config.go`:

```go
type Mastermind struct {
	DatabaseURL       string        `envconfig:"DATABASE_URL" required:"true"`
	GRPCListenAddr    string        `envconfig:"GRPC_LISTEN_ADDR" default:":50051"`
	HTTPListenAddr    string        `envconfig:"HTTP_LISTEN_ADDR" default:":8080"`
	WorkerToken       string        `envconfig:"WORKER_TOKEN" required:"true"`
	AdminUser         string        `envconfig:"ADMIN_USER" required:"true"`
	AdminPassword     string        `envconfig:"ADMIN_PASSWORD" required:"true"`
	AdminToken        string        `envconfig:"ADMIN_TOKEN" required:"true"`
	VisibilityTimeout time.Duration `envconfig:"VISIBILITY_TIMEOUT" default:"30s"`
	ReaperInterval    time.Duration `envconfig:"REAPER_INTERVAL" default:"10s"`
	LogLevel          string        `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat         string        `envconfig:"LOG_FORMAT" default:"json"`
}
```

Then in `LoadMastermind`, after the `AdminPassword` guard, add:

```go
	if c.AdminToken == "" {
		return c, fmt.Errorf("required key ADMIN_TOKEN missing value")
	}
```

- [ ] **Step 5: Run the new tests and see them pass**

Run: `go test ./internal/config/... -run AdminToken -v`
Expected: both tests PASS.

- [ ] **Step 6: Run the full config package tests**

Run: `go test ./internal/config/...`
Expected: PASS. Existing tests either already set every required var or the added required field will make them fail — if so, add `t.Setenv("ADMIN_TOKEN", "…")` to each failing one.

- [ ] **Step 7: Update `make run-mastermind` in the Makefile**

Edit `Makefile`, replacing the `run-mastermind` target so developers don't hit the new required var:

```makefile
run-mastermind:
	DATABASE_URL=$(DATABASE_URL) \
	WORKER_TOKEN=devtoken \
	ADMIN_TOKEN=devtoken \
	ADMIN_USER=admin ADMIN_PASSWORD=admin \
	go run ./cmd/mastermind
```

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go Makefile
git commit -m "feat(config): add required ADMIN_TOKEN for mastermind"
```

---

## Task 2: Per-method bearer interceptor (TDD)

**Files:**
- Modify: `internal/auth/grpc.go`
- Modify: `internal/auth/grpc_test.go`

Add `PerMethodInterceptor` as a parallel API to `BearerInterceptor`. Keep `BearerInterceptor` unchanged — existing callers and tests depend on it.

- [ ] **Step 1: Read existing auth tests**

Run: read `internal/auth/grpc_test.go` to match the style (fake `UnaryServerInfo`, constant-time comparison assumptions, etc.).

- [ ] **Step 2: Write the failing tests**

Append to `internal/auth/grpc_test.go`:

```go
func TestPerMethodInterceptor_RoutesByMethod(t *testing.T) {
	policy := map[string]string{
		"/pkg.Svc/Worker": "w-tok",
		"/pkg.Svc/Admin":  "a-tok",
	}
	itc := PerMethodInterceptor(policy)

	call := func(method, tok string) error {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+tok))
		_, err := itc(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(ctx context.Context, req any) (any, error) { return "ok", nil })
		return err
	}

	if err := call("/pkg.Svc/Worker", "w-tok"); err != nil {
		t.Errorf("worker happy path: %v", err)
	}
	if err := call("/pkg.Svc/Admin", "a-tok"); err != nil {
		t.Errorf("admin happy path: %v", err)
	}
	if err := call("/pkg.Svc/Worker", "a-tok"); status.Code(err) != codes.Unauthenticated {
		t.Errorf("worker method with admin token: code=%v want Unauthenticated", status.Code(err))
	}
	if err := call("/pkg.Svc/Admin", "w-tok"); status.Code(err) != codes.Unauthenticated {
		t.Errorf("admin method with worker token: code=%v want Unauthenticated", status.Code(err))
	}
	if err := call("/pkg.Svc/Unknown", "w-tok"); status.Code(err) != codes.Unimplemented {
		t.Errorf("unknown method: code=%v want Unimplemented", status.Code(err))
	}

	// Missing metadata entirely.
	_, err := itc(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Worker"},
		func(ctx context.Context, req any) (any, error) { return "ok", nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing metadata: code=%v want Unauthenticated", status.Code(err))
	}
}
```

Make sure these imports exist in the test file: `"context"`, `"testing"`, `"google.golang.org/grpc"`, `"google.golang.org/grpc/codes"`, `"google.golang.org/grpc/metadata"`, `"google.golang.org/grpc/status"`.

- [ ] **Step 3: Run the test and see it fail**

Run: `go test ./internal/auth/... -run PerMethod -v`
Expected: compile error — `PerMethodInterceptor` undefined.

- [ ] **Step 4: Implement `PerMethodInterceptor`**

Append to `internal/auth/grpc.go`:

```go
// PerMethodInterceptor returns a unary server interceptor that validates the
// "authorization: Bearer <token>" metadata against a per-method expected token.
// Methods not present in policy receive codes.Unimplemented — this is a
// deliberate fail-closed default so forgetting to wire a method up cannot
// leak an unauthenticated call path.
func PerMethodInterceptor(policy map[string]string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		expected, ok := policy[info.FullMethod]
		if !ok {
			return nil, status.Errorf(codes.Unimplemented, "method %s has no auth policy", info.FullMethod)
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		got := strings.TrimPrefix(vals[0], "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(ctx, req)
	}
}
```

- [ ] **Step 5: Run the test and see it pass**

Run: `go test ./internal/auth/... -run PerMethod -v`
Expected: PASS.

- [ ] **Step 6: Run the whole auth package**

Run: `go test ./internal/auth/...`
Expected: PASS. The existing `BearerInterceptor` tests are untouched.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/grpc.go internal/auth/grpc_test.go
git commit -m "feat(auth): add per-method bearer interceptor"
```

---

## Task 3: Extend `tasks.proto` with `AdminService`

**Files:**
- Modify: `internal/proto/tasks.proto`
- Regenerate: `internal/proto/tasks.pb.go`
- Regenerate: `internal/proto/tasks_grpc.pb.go`

This task doesn't add its own test — the regen produces compilable code, and Task 4 exercises it.

- [ ] **Step 1: Add the import and service to the proto**

Edit `internal/proto/tasks.proto`. After the existing `package` line add the timestamp import, then append the new service and messages at the bottom of the file:

```proto
import "google/protobuf/timestamp.proto";
```

At the bottom of the file (after the existing `ReportResultResponse` message):

```proto
// AdminService is the administrative surface of mastermind. It is consumed by
// the mastermind-mcp binary (on behalf of a coding agent) and any future admin
// tooling. Authentication uses ADMIN_TOKEN, enforced by the server's
// per-method bearer interceptor.
service AdminService {
  // CreateTask inserts a new task in `pending` state and returns it.
  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);

  // GetTask returns one task by id. NOT_FOUND when the id is unknown.
  rpc GetTask(GetTaskRequest) returns (GetTaskResponse);

  // ListTasks returns a page of tasks, optionally filtered by status.
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
}

// TaskDetail mirrors the mastermind `tasks` row. Nullable columns become empty
// strings or zero timestamps on the wire; the MCP adapter omits them when
// serializing to JSON.
message TaskDetail {
  string id                                      = 1;
  string name                                    = 2;
  bytes  payload                                 = 3;
  int32  priority                                = 4;
  string status                                  = 5;
  google.protobuf.Timestamp scheduled_for        = 6;
  int32  attempt_count                           = 7;
  int32  max_attempts                            = 8;
  string claimed_by                              = 9;
  google.protobuf.Timestamp claimed_at           = 10;
  google.protobuf.Timestamp last_heartbeat_at    = 11;
  google.protobuf.Timestamp completed_at         = 12;
  string last_error                              = 13;
  string jira_url                                = 14;
  string github_pr_url                           = 15;
  google.protobuf.Timestamp created_at           = 16;
  google.protobuf.Timestamp updated_at           = 17;
}

message CreateTaskRequest {
  string name                                    = 1;
  bytes  payload                                 = 2;
  int32  priority                                = 3;
  int32  max_attempts                            = 4;
  google.protobuf.Timestamp scheduled_for        = 5;
  string jira_url                                = 6;
  string github_pr_url                           = 7;
}
message CreateTaskResponse { TaskDetail task = 1; }

message GetTaskRequest  { string id = 1; }
message GetTaskResponse { TaskDetail task = 1; }

message ListTasksRequest {
  string status = 1;
  int32  limit  = 2;
  int32  offset = 3;
}
message ListTasksResponse {
  repeated TaskDetail items = 1;
  int32             total   = 2;
}
```

- [ ] **Step 2: Regenerate protobuf code**

Run: `make proto`
Expected: no output; `internal/proto/tasks.pb.go` and `internal/proto/tasks_grpc.pb.go` are updated.

If the `google/protobuf/timestamp.proto` import can't be resolved, either install `protoc-gen-go`'s well-known types (usually already included) or verify your `protoc` include path — the repo already uses it-less generation, so this is the one moment to check. Command to inspect: `protoc --version && which protoc`.

- [ ] **Step 3: Build the module to confirm the generated code compiles**

Run: `go build ./...`
Expected: success. New types (`AdminServiceServer`, `AdminServiceClient`, `TaskDetail`, `CreateTaskRequest`, `CreateTaskResponse`, `GetTaskRequest`, `GetTaskResponse`, `ListTasksRequest`, `ListTasksResponse`) are now exported from `internal/proto`.

- [ ] **Step 4: Run the full test suite**

Run: `make test`
Expected: PASS. Nothing references the new types yet, so the generated code is purely additive.

- [ ] **Step 5: Commit**

```bash
git add internal/proto/tasks.proto internal/proto/tasks.pb.go internal/proto/tasks_grpc.pb.go
git commit -m "feat(proto): add AdminService and TaskDetail messages"
```

---

## Task 4a: `AdminService.CreateTask` handler (TDD)

**Files:**
- Create: `internal/mastermind/grpcserver/admin.go`
- Create: `internal/mastermind/grpcserver/admin_test.go`

Build the admin server incrementally — `CreateTask` first, because later RPC tests can use it to set up fixtures.

- [ ] **Step 1: Create the admin test helper**

File: `internal/mastermind/grpcserver/admin_test.go`

```go
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
		"/elpulpo.tasks.v1.TaskService/ClaimTask":     testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":     testWorkerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult":  testWorkerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":   testAdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":      testAdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":    testAdminToken,
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
```

- [ ] **Step 2: Write the CreateTask failing tests**

Append to `internal/mastermind/grpcserver/admin_test.go`:

```go
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
```

- [ ] **Step 3: Run the tests and see them fail**

Run: `go test ./internal/mastermind/grpcserver/... -run TestCreateTask -v`
Expected: compile error — `NewAdmin` and `AdminServiceServer` registration are not defined.

- [ ] **Step 4: Create `admin.go` with `NewAdmin` and `CreateTask`**

File: `internal/mastermind/grpcserver/admin.go`

```go
package grpcserver

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// AdminServer implements the AdminService RPCs by delegating to the store.
// No new SQL is introduced — every call is one existing store method.
type AdminServer struct {
	pb.UnimplementedAdminServiceServer
	store *store.Store
}

func NewAdmin(s *store.Store) *AdminServer { return &AdminServer{store: s} }

const maxNameLen = 200

func (a *AdminServer) CreateTask(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if len(name) > maxNameLen {
		return nil, status.Errorf(codes.InvalidArgument, "name too long (max %d)", maxNameLen)
	}
	if req.GetMaxAttempts() < 0 || req.GetMaxAttempts() > 50 {
		return nil, status.Error(codes.InvalidArgument, "max_attempts must be 0 (default) or 1..50")
	}

	var payload json.RawMessage
	if len(req.GetPayload()) > 0 {
		payload = json.RawMessage(req.GetPayload())
		var tmp any
		if err := json.Unmarshal(payload, &tmp); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "payload is not valid JSON: %v", err)
		}
	}

	in := store.NewTaskInput{
		Name:        name,
		Payload:     payload,
		Priority:    int(req.GetPriority()),
		MaxAttempts: int(req.GetMaxAttempts()),
	}
	if sf := req.GetScheduledFor(); sf != nil {
		t := sf.AsTime()
		in.ScheduledFor = &t
	}
	if v := req.GetJiraUrl(); v != "" {
		in.JiraURL = &v
	}
	if v := req.GetGithubPrUrl(); v != "" {
		in.GithubPRURL = &v
	}

	t, err := a.store.CreateTask(ctx, in)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create: %v", err)
	}
	return &pb.CreateTaskResponse{Task: toTaskDetail(t)}, nil
}

// toTaskDetail converts a store.Task into the proto TaskDetail. Nullable
// columns become empty strings or nil timestamps; MCP layer downstream omits
// them when serializing to JSON.
func toTaskDetail(t store.Task) *pb.TaskDetail {
	d := &pb.TaskDetail{
		Id:           t.ID.String(),
		Name:         t.Name,
		Payload:      []byte(t.Payload),
		Priority:     int32(t.Priority),
		Status:       string(t.Status),
		AttemptCount: int32(t.AttemptCount),
		MaxAttempts:  int32(t.MaxAttempts),
		CreatedAt:    timestamppb.New(t.CreatedAt),
		UpdatedAt:    timestamppb.New(t.UpdatedAt),
	}
	if t.ScheduledFor != nil {
		d.ScheduledFor = timestamppb.New(*t.ScheduledFor)
	}
	if t.ClaimedBy != nil {
		d.ClaimedBy = *t.ClaimedBy
	}
	if t.ClaimedAt != nil {
		d.ClaimedAt = timestamppb.New(*t.ClaimedAt)
	}
	if t.LastHeartbeatAt != nil {
		d.LastHeartbeatAt = timestamppb.New(*t.LastHeartbeatAt)
	}
	if t.CompletedAt != nil {
		d.CompletedAt = timestamppb.New(*t.CompletedAt)
	}
	if t.LastError != nil {
		d.LastError = *t.LastError
	}
	if t.JiraURL != nil {
		d.JiraUrl = *t.JiraURL
	}
	if t.GithubPRURL != nil {
		d.GithubPrUrl = *t.GithubPRURL
	}
	return d
}
```

- [ ] **Step 5: Run the tests and see them pass**

Run: `go test ./internal/mastermind/grpcserver/... -run TestCreateTask -v`
Expected: all five `TestCreateTask_*` PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/mastermind/grpcserver/admin.go internal/mastermind/grpcserver/admin_test.go
git commit -m "feat(grpcserver): implement AdminService.CreateTask"
```

---

## Task 4b: `AdminService.GetTask` handler (TDD)

**Files:**
- Modify: `internal/mastermind/grpcserver/admin.go`
- Modify: `internal/mastermind/grpcserver/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mastermind/grpcserver/admin_test.go`:

```go
func TestGetTask_Happy(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	created, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"})
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
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/mastermind/grpcserver/... -run TestGetTask -v`
Expected: compile error — `GetTask` is not implemented (embedded `UnimplementedAdminServiceServer` would return `Unimplemented`, which doesn't match any expectation).

- [ ] **Step 3: Implement `GetTask`**

In `internal/mastermind/grpcserver/admin.go`, add two imports the handler needs:

```go
import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)
```

Then append the handler:

```go
func (a *AdminServer) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	t, err := a.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "task %s not found", id)
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	return &pb.GetTaskResponse{Task: toTaskDetail(t)}, nil
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/mastermind/grpcserver/... -run TestGetTask -v`
Expected: all three `TestGetTask_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/grpcserver/admin.go internal/mastermind/grpcserver/admin_test.go
git commit -m "feat(grpcserver): implement AdminService.GetTask"
```

---

## Task 4c: `AdminService.ListTasks` handler (TDD)

**Files:**
- Modify: `internal/mastermind/grpcserver/admin.go`
- Modify: `internal/mastermind/grpcserver/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mastermind/grpcserver/admin_test.go`:

```go
func TestListTasks_EmptyFilter(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	for i := 0; i < 3; i++ {
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"}); err != nil {
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
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"}); err != nil {
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

func TestListTasks_Pagination(t *testing.T) {
	admin, _, _ := startAdminBufServer(t)
	for i := 0; i < 5; i++ {
		if _, err := admin.CreateTask(adminCtx(), &pb.CreateTaskRequest{Name: "t"}); err != nil {
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
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/mastermind/grpcserver/... -run TestListTasks -v`
Expected: `ListTasks` returns `Unimplemented` from the embedded base type.

- [ ] **Step 3: Implement `ListTasks`**

In `internal/mastermind/grpcserver/admin.go`, append:

```go
var knownStatuses = map[string]store.TaskStatus{
	"pending":   store.StatusPending,
	"claimed":   store.StatusClaimed,
	"running":   store.StatusRunning,
	"completed": store.StatusCompleted,
	"failed":    store.StatusFailed,
}

func (a *AdminServer) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	f := store.ListTasksFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if req.GetLimit() < 0 || req.GetLimit() > 500 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 0..500")
	}
	if req.GetOffset() < 0 {
		return nil, status.Error(codes.InvalidArgument, "offset must be non-negative")
	}
	if s := req.GetStatus(); s != "" {
		ks, ok := knownStatuses[s]
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown status %q", s)
		}
		f.Status = &ks
	}
	page, err := a.store.ListTasks(ctx, f)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	out := &pb.ListTasksResponse{Total: int32(page.Total)}
	for _, t := range page.Items {
		out.Items = append(out.Items, toTaskDetail(t))
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/mastermind/grpcserver/... -run TestListTasks -v`
Expected: all four `TestListTasks_*` PASS.

- [ ] **Step 5: Run the full grpcserver package**

Run: `go test ./internal/mastermind/grpcserver/...`
Expected: PASS. Existing `TaskService` tests still use their original `startBufServer` helper with no interceptor; they are unaffected.

- [ ] **Step 6: Commit**

```bash
git add internal/mastermind/grpcserver/admin.go internal/mastermind/grpcserver/admin_test.go
git commit -m "feat(grpcserver): implement AdminService.ListTasks"
```

---

## Task 5: Wire `AdminService` into the mastermind binary

**Files:**
- Modify: `cmd/mastermind/main.go`

- [ ] **Step 1: Read current `main.go`**

Run: read `cmd/mastermind/main.go` and note the current `grpc.NewServer(grpc.UnaryInterceptor(auth.BearerInterceptor(cfg.WorkerToken)))` line and `pb.RegisterTaskServiceServer` call.

- [ ] **Step 2: Replace the single-token interceptor with the per-method policy**

Edit `cmd/mastermind/main.go`. Replace:

```go
	gs := grpc.NewServer(grpc.UnaryInterceptor(auth.BearerInterceptor(cfg.WorkerToken)))
	pb.RegisterTaskServiceServer(gs, grpcserver.New(s))
```

With:

```go
	policy := map[string]string{
		"/elpulpo.tasks.v1.TaskService/ClaimTask":    cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":    cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult": cfg.WorkerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":  cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":     cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":   cfg.AdminToken,
	}
	gs := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterTaskServiceServer(gs, grpcserver.New(s))
	pb.RegisterAdminServiceServer(gs, grpcserver.NewAdmin(s))
```

- [ ] **Step 3: Build**

Run: `go build ./cmd/mastermind`
Expected: success.

- [ ] **Step 4: Run the full test suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Smoke test the binary locally**

In one terminal: `make dev-up && make migrate-up && make run-mastermind`
Expected: mastermind starts. In another terminal, confirm it is listening: `lsof -iTCP:50051 -sTCP:LISTEN`. Then `Ctrl-C` the mastermind.

- [ ] **Step 6: Commit**

```bash
git add cmd/mastermind/main.go
git commit -m "feat(mastermind): register AdminService with per-method auth"
```

---

## Task 6: Add the official Go MCP SDK dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

Run:

```bash
go get github.com/modelcontextprotocol/go-sdk@latest
go mod tidy
```

Expected: `github.com/modelcontextprotocol/go-sdk` appears as a top-level require in `go.mod`, and `go.sum` is updated.

- [ ] **Step 2: Confirm it resolves cleanly**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add modelcontextprotocol/go-sdk"
```

---

## Task 7: `internal/mcpserver/config.go` — env + flag overrides (TDD)

**Files:**
- Create: `internal/mcpserver/config.go`
- Create: `internal/mcpserver/config_test.go`

- [ ] **Step 1: Write the failing tests**

File: `internal/mcpserver/config_test.go`

```go
package mcpserver

import (
	"testing"
	"time"
)

func TestLoad_EnvOnly(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "localhost:50051")
	t.Setenv("ADMIN_TOKEN", "tok")

	c, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MastermindAddr != "localhost:50051" {
		t.Errorf("Addr=%q", c.MastermindAddr)
	}
	if c.AdminToken != "tok" {
		t.Errorf("Token=%q", c.AdminToken)
	}
	if c.DialTimeout != 5*time.Second {
		t.Errorf("DialTimeout=%v, want 5s", c.DialTimeout)
	}
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "env-addr:1")
	t.Setenv("ADMIN_TOKEN", "env-tok")

	c, err := Load([]string{"--addr", "flag-addr:2", "--token", "flag-tok", "--tls", "--dial-timeout", "1s"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MastermindAddr != "flag-addr:2" {
		t.Errorf("Addr=%q, want flag-addr:2", c.MastermindAddr)
	}
	if c.AdminToken != "flag-tok" {
		t.Errorf("Token=%q, want flag-tok", c.AdminToken)
	}
	if !c.TLS {
		t.Error("TLS should be true")
	}
	if c.DialTimeout != time.Second {
		t.Errorf("DialTimeout=%v, want 1s", c.DialTimeout)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "")
	t.Setenv("ADMIN_TOKEN", "")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("want error for missing MASTERMIND_ADDR / ADMIN_TOKEN")
	}
}
```

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/mcpserver/... -run TestLoad -v`
Expected: compile error — `Load` undefined.

- [ ] **Step 3: Implement `Load`**

File: `internal/mcpserver/config.go`

```go
// Package mcpserver wires the mastermind admin gRPC client to the official
// MCP Go SDK, registering tools that let a coding agent create and inspect
// tasks on mastermind.
package mcpserver

import (
	"flag"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config is the runtime configuration for the mastermind-mcp binary.
type Config struct {
	MastermindAddr string        `envconfig:"MASTERMIND_ADDR"`
	AdminToken     string        `envconfig:"ADMIN_TOKEN"`
	TLS            bool          `envconfig:"MASTERMIND_TLS" default:"false"`
	DialTimeout    time.Duration `envconfig:"DIAL_TIMEOUT" default:"5s"`
	LogLevel       string        `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat      string        `envconfig:"LOG_FORMAT" default:"json"`
}

// Load reads config from the environment, then applies CLI flag overrides.
// Passing nil args loads env only (useful for tests and library consumers).
func Load(args []string) (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	fs := flag.NewFlagSet("mastermind-mcp", flag.ContinueOnError)
	fs.StringVar(&c.MastermindAddr, "addr", c.MastermindAddr, "mastermind gRPC address (env: MASTERMIND_ADDR)")
	fs.StringVar(&c.AdminToken, "token", c.AdminToken, "admin bearer token (env: ADMIN_TOKEN)")
	fs.BoolVar(&c.TLS, "tls", c.TLS, "dial mastermind with TLS (env: MASTERMIND_TLS)")
	fs.DurationVar(&c.DialTimeout, "dial-timeout", c.DialTimeout, "startup dial deadline (env: DIAL_TIMEOUT)")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "log level (env: LOG_LEVEL)")
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "log format json|text (env: LOG_FORMAT)")
	if args != nil {
		if err := fs.Parse(args); err != nil {
			return c, err
		}
	}
	if c.MastermindAddr == "" {
		return c, fmt.Errorf("MASTERMIND_ADDR (or --addr) is required")
	}
	if c.AdminToken == "" {
		return c, fmt.Errorf("ADMIN_TOKEN (or --token) is required")
	}
	return c, nil
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/mcpserver/... -run TestLoad -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/config.go internal/mcpserver/config_test.go
git commit -m "feat(mcpserver): env + flag override config"
```

---

## Task 8: `internal/mcpserver` tests scaffold (testcontainers + bufconn)

**Files:**
- Create: `internal/mcpserver/testmain_test.go`

`mcpserver` tests stand up a real Postgres (testcontainers) and a bufconn gRPC server with `AdminService` registered. Factor the heavy setup into `TestMain` so the test file that follows can stay focused.

- [ ] **Step 1: Create `testmain_test.go`**

File: `internal/mcpserver/testmain_test.go`

```go
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
```

- [ ] **Step 2: Verify the setup compiles**

Run: `go test ./internal/mcpserver/... -run=$^`
Expected: success (no tests matched, zero failures). If it fails to compile, fix imports before moving on.

- [ ] **Step 3: Commit**

```bash
git add internal/mcpserver/testmain_test.go
git commit -m "test(mcpserver): add testcontainers + bufconn harness"
```

---

## Task 9a: MCP `create_task` tool (TDD)

**Files:**
- Create: `internal/mcpserver/server.go`
- Create: `internal/mcpserver/tools.go`
- Create: `internal/mcpserver/tools_test.go`

Tools are registered in `server.go`; handlers live in `tools.go`; both are exercised in `tools_test.go`. This first slice implements `create_task` end-to-end.

- [ ] **Step 1: Write the failing test**

File: `internal/mcpserver/tools_test.go`

```go
package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// startMCPClient builds the MCP server wired to the provided AdminService
// client, connects a matching MCP client over an in-memory transport, and
// returns the client session.
func startMCPClient(t *testing.T, admin pb.AdminServiceClient) *mcp.ClientSession {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()

	srv := NewServer(admin)
	go func() { _ = srv.Run(context.Background(), serverT) }()

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := c.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestCreateTaskTool_Happy(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{"name": "build", "priority": 5},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}

	// Structured content should be JSON-decodable to TaskDetail shape.
	if res.StructuredContent == nil {
		t.Fatal("no structured content")
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Name     string `json:"name"`
		Priority int    `json:"priority"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
	if out.Name != "build" || out.Priority != 5 || out.Status != "pending" {
		t.Errorf("got %+v, want name=build priority=5 status=pending", out)
	}
}

func TestCreateTaskTool_MissingName_ToolError(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool (protocol): %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for missing name")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/mcpserver/... -run TestCreateTaskTool -v`
Expected: compile error — `NewServer` undefined.

- [ ] **Step 3: Create `server.go` and `tools.go` with `create_task`**

File: `internal/mcpserver/server.go`

```go
package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// NewServer builds an MCP server wired to the given AdminService client and
// registers every tool the mastermind-mcp binary exposes.
func NewServer(admin pb.AdminServiceClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mastermind-mcp", Version: "v1.0.0"}, nil)
	registerCreateTask(s, admin)
	return s
}
```

File: `internal/mcpserver/tools.go`

```go
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// TaskDetail is the JSON shape the MCP tools return. Field tags use
// snake_case to match the MCP convention; optional fields use `omitempty` so
// an unclaimed task doesn't carry empty claim metadata.
type TaskDetail struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Payload         json.RawMessage `json:"payload"`
	Priority        int32           `json:"priority"`
	Status          string          `json:"status"`
	ScheduledFor    *time.Time      `json:"scheduled_for,omitempty"`
	AttemptCount    int32           `json:"attempt_count"`
	MaxAttempts     int32           `json:"max_attempts"`
	ClaimedBy       string          `json:"claimed_by,omitempty"`
	ClaimedAt       *time.Time      `json:"claimed_at,omitempty"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	LastError       string          `json:"last_error,omitempty"`
	JiraURL         string          `json:"jira_url,omitempty"`
	GithubPRURL     string          `json:"github_pr_url,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func fromProtoTask(p *pb.TaskDetail) TaskDetail {
	d := TaskDetail{
		ID:           p.GetId(),
		Name:         p.GetName(),
		Payload:      p.GetPayload(),
		Priority:     p.GetPriority(),
		Status:       p.GetStatus(),
		AttemptCount: p.GetAttemptCount(),
		MaxAttempts:  p.GetMaxAttempts(),
		ClaimedBy:    p.GetClaimedBy(),
		LastError:    p.GetLastError(),
		JiraURL:      p.GetJiraUrl(),
		GithubPRURL:  p.GetGithubPrUrl(),
		CreatedAt:    p.GetCreatedAt().AsTime(),
		UpdatedAt:    p.GetUpdatedAt().AsTime(),
	}
	if len(d.Payload) == 0 {
		d.Payload = json.RawMessage("{}")
	}
	if t := p.GetScheduledFor(); t.IsValid() {
		tt := t.AsTime()
		d.ScheduledFor = &tt
	}
	if t := p.GetClaimedAt(); t.IsValid() {
		tt := t.AsTime()
		d.ClaimedAt = &tt
	}
	if t := p.GetLastHeartbeatAt(); t.IsValid() {
		tt := t.AsTime()
		d.LastHeartbeatAt = &tt
	}
	if t := p.GetCompletedAt(); t.IsValid() {
		tt := t.AsTime()
		d.CompletedAt = &tt
	}
	return d
}

// CreateTaskInput is the MCP tool input for create_task. The SDK derives the
// JSON schema from these struct tags.
type CreateTaskInput struct {
	Name         string          `json:"name" jsonschema:"the task type (required), 1-200 chars"`
	Payload      json.RawMessage `json:"payload,omitempty" jsonschema:"opaque JSON payload, default {}"`
	Priority     int32           `json:"priority,omitempty" jsonschema:"priority, default 0 (higher runs first)"`
	MaxAttempts  int32           `json:"max_attempts,omitempty" jsonschema:"max attempts, default 3, range 1-50"`
	ScheduledFor *time.Time      `json:"scheduled_for,omitempty" jsonschema:"earliest time the task is eligible to run (RFC3339)"`
	JiraURL      string          `json:"jira_url,omitempty" jsonschema:"optional JIRA issue URL"`
	GithubPRURL  string          `json:"github_pr_url,omitempty" jsonschema:"optional GitHub pull request URL"`
}

func registerCreateTask(s *mcp.Server, admin pb.AdminServiceClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new task in the mastermind queue. Returns the created task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateTaskInput) (*mcp.CallToolResult, TaskDetail, error) {
		req := &pb.CreateTaskRequest{
			Name:        in.Name,
			Payload:     []byte(in.Payload),
			Priority:    in.Priority,
			MaxAttempts: in.MaxAttempts,
			JiraUrl:     in.JiraURL,
			GithubPrUrl: in.GithubPRURL,
		}
		if in.ScheduledFor != nil {
			req.ScheduledFor = timestamppb.New(*in.ScheduledFor)
		}
		resp, err := admin.CreateTask(ctx, req)
		if err != nil {
			return toolErr(err, "create_task"), TaskDetail{}, nil
		}
		d := fromProtoTask(resp.GetTask())
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Created task %s (%s)", d.ID, d.Name),
			}},
		}, d, nil
	})
}

// toolErr converts a gRPC error from mastermind into an MCP tool error.
// We always return tool errors (IsError=true) rather than protocol errors —
// the MCP server itself should never fail a call just because an RPC didn't.
func toolErr(err error, tool string) *mcp.CallToolResult {
	st, _ := status.FromError(err)
	var msg string
	switch st.Code() {
	case codes.InvalidArgument, codes.NotFound:
		msg = st.Message()
	case codes.Unauthenticated:
		msg = "mastermind rejected admin token"
	case codes.Unavailable:
		msg = "mastermind unreachable"
	default:
		msg = "internal error"
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %s", tool, msg)}},
	}
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go test ./internal/mcpserver/... -run TestCreateTaskTool -v`
Expected: both `TestCreateTaskTool_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/tools.go internal/mcpserver/tools_test.go
git commit -m "feat(mcpserver): add create_task tool"
```

---

## Task 9b: MCP `get_task` tool (TDD)

**Files:**
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/tools.go`
- Modify: `internal/mcpserver/tools_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/tools_test.go`:

```go
func TestGetTaskTool_Happy(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	created, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{"name": "x"},
	})
	if err != nil || created.IsError {
		t.Fatalf("seed CreateTask: %v %+v", err, created)
	}
	raw, _ := json.Marshal(created.StructuredContent)
	var seed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &seed)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": seed.ID},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw2, _ := json.Marshal(res.StructuredContent)
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw2, &out)
	if out.ID != seed.ID || out.Name != "x" {
		t.Errorf("got id=%q name=%q, want id=%q name=x", out.ID, out.Name, seed.ID)
	}
}

func TestGetTaskTool_NotFound_ToolError(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": "00000000-0000-0000-0000-000000000000"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("want IsError=true for missing id")
	}
}
```

- [ ] **Step 2: Run and see it fail**

Run: `go test ./internal/mcpserver/... -run TestGetTaskTool -v`
Expected: the MCP server has no `get_task` tool, so the call returns an MCP protocol error or tool error of the wrong shape.

- [ ] **Step 3: Register `get_task`**

In `internal/mcpserver/server.go`, update `NewServer`:

```go
func NewServer(admin pb.AdminServiceClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mastermind-mcp", Version: "v1.0.0"}, nil)
	registerCreateTask(s, admin)
	registerGetTask(s, admin)
	return s
}
```

In `internal/mcpserver/tools.go`, append:

```go
type GetTaskInput struct {
	ID string `json:"id" jsonschema:"task id (UUID)"`
}

func registerGetTask(s *mcp.Server, admin pb.AdminServiceClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_task",
		Description: "Fetch one task by id. Returns an error if the id is unknown.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetTaskInput) (*mcp.CallToolResult, TaskDetail, error) {
		resp, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: in.ID})
		if err != nil {
			return toolErr(err, "get_task"), TaskDetail{}, nil
		}
		d := fromProtoTask(resp.GetTask())
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("%s — %s", d.ID, d.Status),
			}},
		}, d, nil
	})
}
```

- [ ] **Step 4: Run and see the tests pass**

Run: `go test ./internal/mcpserver/... -run TestGetTaskTool -v`
Expected: both `TestGetTaskTool_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/tools.go internal/mcpserver/tools_test.go
git commit -m "feat(mcpserver): add get_task tool"
```

---

## Task 9c: MCP `list_tasks` tool (TDD)

**Files:**
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/tools.go`
- Modify: `internal/mcpserver/tools_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mcpserver/tools_test.go`:

```go
func TestListTasksTool_Happy(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	for i := 0; i < 3; i++ {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "create_task",
			Arguments: map[string]any{"name": "x"},
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tasks",
		Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("ListTasks: %v %+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Items []TaskDetail `json:"items"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 3 || len(out.Items) != 3 {
		t.Errorf("got total=%d items=%d, want 3/3", out.Total, len(out.Items))
	}
}

func TestListTasksTool_BadStatus_ToolError(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tasks",
		Arguments: map[string]any{"status": "bogus"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for bad status")
	}
}
```

- [ ] **Step 2: Run and see it fail**

Run: `go test ./internal/mcpserver/... -run TestListTasksTool -v`
Expected: `list_tasks` tool not registered yet.

- [ ] **Step 3: Register `list_tasks`**

In `internal/mcpserver/server.go`, update `NewServer`:

```go
func NewServer(admin pb.AdminServiceClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mastermind-mcp", Version: "v1.0.0"}, nil)
	registerCreateTask(s, admin)
	registerGetTask(s, admin)
	registerListTasks(s, admin)
	return s
}
```

In `internal/mcpserver/tools.go`, append:

```go
type ListTasksInput struct {
	Status string `json:"status,omitempty" jsonschema:"filter: pending|claimed|running|completed|failed"`
	Limit  int32  `json:"limit,omitempty" jsonschema:"page size, default 50, max 500"`
	Offset int32  `json:"offset,omitempty" jsonschema:"pagination offset, default 0"`
}

type ListTasksOutput struct {
	Items []TaskDetail `json:"items"`
	Total int32        `json:"total"`
}

func registerListTasks(s *mcp.Server, admin pb.AdminServiceClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List tasks, optionally filtered by status. Paginated.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListTasksInput) (*mcp.CallToolResult, ListTasksOutput, error) {
		resp, err := admin.ListTasks(ctx, &pb.ListTasksRequest{
			Status: in.Status,
			Limit:  in.Limit,
			Offset: in.Offset,
		})
		if err != nil {
			return toolErr(err, "list_tasks"), ListTasksOutput{}, nil
		}
		out := ListTasksOutput{Total: resp.GetTotal()}
		for _, p := range resp.GetItems() {
			out.Items = append(out.Items, fromProtoTask(p))
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("%d of %d tasks", len(out.Items), out.Total),
			}},
		}, out, nil
	})
}
```

- [ ] **Step 4: Run and see the tests pass**

Run: `go test ./internal/mcpserver/... -run TestListTasksTool -v`
Expected: both `TestListTasksTool_*` PASS.

- [ ] **Step 5: Run the full mcpserver package**

Run: `go test ./internal/mcpserver/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/tools.go internal/mcpserver/tools_test.go
git commit -m "feat(mcpserver): add list_tasks tool"
```

---

## Task 10: `cmd/mastermind-mcp/main.go` — the binary

**Files:**
- Create: `cmd/mastermind-mcp/main.go`

- [ ] **Step 1: Write `main.go`**

File: `cmd/mastermind-mcp/main.go`

```go
// Command mastermind-mcp is a stdio MCP server that exposes mastermind's
// AdminService as MCP tools. Spawned as a subprocess by a coding agent.
//
// stdout is the MCP framing channel; all logs MUST go to stderr.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mcpserver"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mastermind-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := mcpserver.Load(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	log := newLogger(cfg.LogLevel, cfg.LogFormat)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	var tc credentials.TransportCredentials
	if cfg.TLS {
		tc = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		tc = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(cfg.MastermindAddr,
		grpc.WithTransportCredentials(tc),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(cfg.AdminToken)))
	if err != nil {
		return fmt.Errorf("dial %s: %w", cfg.MastermindAddr, err)
	}
	defer conn.Close()

	// Startup probe: one tiny ListTasks call within dialCtx. Fails fast on
	// unreachable mastermind or a rejected admin token, before the coding
	// agent ever sees the MCP handshake succeed.
	client := pb.NewAdminServiceClient(conn)
	if _, err := client.ListTasks(dialCtx, &pb.ListTasksRequest{Limit: 1}); err != nil {
		return fmt.Errorf("probe mastermind: %w", err)
	}

	srv := mcpserver.NewServer(client)
	log.Info("mastermind-mcp: ready",
		"mastermind_addr", cfg.MastermindAddr, "tls", cfg.TLS)

	// Run the MCP stdio loop until stdin EOF, signal, or transport error.
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
```

- [ ] **Step 2: Build the binary**

Run: `go build -o bin/mastermind-mcp ./cmd/mastermind-mcp`
Expected: success.

- [ ] **Step 3: Fail-fast on missing config**

Run: `./bin/mastermind-mcp`
Expected: stderr prints `mastermind-mcp: MASTERMIND_ADDR (or --addr) is required`; exit code 1.

- [ ] **Step 4: Fail-fast on unreachable mastermind**

Run: `MASTERMIND_ADDR=127.0.0.1:1 ADMIN_TOKEN=x DIAL_TIMEOUT=500ms ./bin/mastermind-mcp`
Expected: stderr prints `mastermind-mcp: probe mastermind: …`; exit code 1 within ~0.5s.

- [ ] **Step 5: Commit**

```bash
git add cmd/mastermind-mcp/main.go
git commit -m "feat(mcp): add mastermind-mcp stdio binary"
```

---

## Task 11: End-to-end stdio smoke test

**Files:**
- Create: `cmd/mastermind-mcp/main_test.go`

Tests the binary for real: builds it, pipes JSON-RPC to its stdin, reads from its stdout, confirms `initialize` + `tools/list` return the three tool names. Uses a real in-process mastermind running on a real TCP port (not bufconn, because the binary dials over the OS socket layer).

- [ ] **Step 1: Write the smoke test**

File: `cmd/mastermind-mcp/main_test.go`

```go
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
```

- [ ] **Step 2: Run the test**

Run: `go test ./cmd/mastermind-mcp/... -v`
Expected: PASS. This test is slow (testcontainers + `go build`); a single run, not per-test.

- [ ] **Step 3: Commit**

```bash
git add cmd/mastermind-mcp/main_test.go
git commit -m "test(mcp): end-to-end stdio smoke test"
```

---

## Task 12: Dockerfile + Makefile updates

**Files:**
- Create: `Dockerfile.mcp`
- Modify: `Makefile`

- [ ] **Step 1: Read the existing mastermind Dockerfile**

Run: read `Dockerfile.mastermind` to copy the layout verbatim (base image pins, build flags, distroless stage, etc.) so the new Dockerfile is consistent.

- [ ] **Step 2: Write `Dockerfile.mcp`**

File: `Dockerfile.mcp`

Use the exact same two-stage pattern as `Dockerfile.mastermind` and `Dockerfile.worker` — same Go base image tag, same `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'` flags, same distroless final stage — but build `./cmd/mastermind-mcp` instead. If `Dockerfile.mastermind` looks like:

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mastermind ./cmd/mastermind

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mastermind /mastermind
COPY migrations /migrations
USER nonroot:nonroot
ENTRYPOINT ["/mastermind"]
```

Then `Dockerfile.mcp` is the same minus the `migrations` copy (the MCP binary does not run migrations), with `mastermind-mcp` substituted for `mastermind`:

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mastermind-mcp ./cmd/mastermind-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mastermind-mcp /mastermind-mcp
USER nonroot:nonroot
ENTRYPOINT ["/mastermind-mcp"]
```

(If `Dockerfile.mastermind` differs in detail — different base image tag, different flags — match it; don't invent a new shape.)

- [ ] **Step 3: Update the Makefile**

Edit `Makefile`. Update the `.PHONY` line to include the new targets, add `run-mcp` and `build-mcp`, and extend `build`:

```makefile
.PHONY: dev-up dev-down migrate-up migrate-down migrate-new \
        proto run-mastermind run-worker run-mcp test tidy build build-mcp
```

Add `run-mcp` after `run-worker`:

```makefile
run-mcp:
	MASTERMIND_ADDR=localhost:50051 \
	ADMIN_TOKEN=devtoken \
	go run ./cmd/mastermind-mcp
```

Replace the `build` target:

```makefile
build:
	CGO_ENABLED=0 go build -o bin/mastermind ./cmd/mastermind
	CGO_ENABLED=0 go build -o bin/worker ./cmd/worker
	CGO_ENABLED=0 go build -o bin/mastermind-mcp ./cmd/mastermind-mcp
```

(Optional `build-mcp` alias that just builds the MCP binary for iterative dev:)

```makefile
build-mcp:
	CGO_ENABLED=0 go build -o bin/mastermind-mcp ./cmd/mastermind-mcp
```

- [ ] **Step 4: Smoke-build**

Run: `make build`
Expected: three binaries under `bin/`.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile.mcp Makefile
git commit -m "build: add Dockerfile.mcp and Makefile targets for mastermind-mcp"
```

---

## Task 13: README MCP integration note

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a new section to README.md**

After the "Admin UI" section, add:

```markdown
## MCP server (coding agent integration)

`mastermind-mcp` is a stdio MCP server that exposes mastermind as tools to a
coding agent (e.g., Claude Code).

Example `.mcp.json`:

```json
{
  "mcpServers": {
    "el-pulpo": {
      "command": "mastermind-mcp",
      "env": {
        "MASTERMIND_ADDR": "localhost:50051",
        "ADMIN_TOKEN":     "…"
      }
    }
  }
}
```

Tools: `create_task`, `get_task`, `list_tasks`. See the design doc for the full input/output schemas.

Keep `ADMIN_TOKEN` out of version control — source it from your shell environment or a secrets manager.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs(readme): document mastermind-mcp integration"
```

---

## Task 14: Final whole-project verification

**Files:** none.

- [ ] **Step 1: Full test run**

Run: `make test`
Expected: PASS (unit + integration).

- [ ] **Step 2: Build everything**

Run: `make build`
Expected: three binaries.

- [ ] **Step 3: Manual end-to-end check**

In terminal A:

```bash
make dev-up
make migrate-up
make run-mastermind
```

In terminal B, drive the MCP binary by hand:

```bash
MASTERMIND_ADDR=localhost:50051 ADMIN_TOKEN=devtoken LOG_LEVEL=error ./bin/mastermind-mcp
```

Paste:

```
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"manual","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_task","arguments":{"name":"manual-check"}}}
```

Expected: stdout emits JSON responses; the mastermind UI at `http://localhost:8080` shows a new `manual-check` task in `pending`.

- [ ] **Step 4: Commit (if anything needed touch-up)**

If the manual test surfaced any fixup, commit it with a focused message. Otherwise nothing to commit.

---

## Done

All tasks completed:
- ✅ Config: `ADMIN_TOKEN` required for mastermind.
- ✅ Auth: `PerMethodInterceptor` in `internal/auth`.
- ✅ Proto: `AdminService` with `CreateTask` / `GetTask` / `ListTasks`.
- ✅ gRPC handlers: delegate to existing `store`, no new SQL.
- ✅ MCP server package: typed tools + gRPC error → tool error mapping.
- ✅ Binary: `cmd/mastermind-mcp` with stdio transport + env/flag config.
- ✅ Tests: unit, integration (bufconn + testcontainers), stdio smoke test.
- ✅ Build: Dockerfile + Makefile + README note.
