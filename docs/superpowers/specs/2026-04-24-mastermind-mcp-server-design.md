# Mastermind MCP Server — Design

**Date:** 2026-04-24
**Status:** Approved for planning
**Language / Runtime:** Go

## 1. Goal

Expose mastermind to a coding agent (e.g., Claude Code) over the Model
Context Protocol so that the agent can create and inspect tasks
programmatically instead of clicking through the HTMX admin UI.

This work introduces:

- A new gRPC `AdminService` on the mastermind, colocated with the
  existing `TaskService`, guarded by its own bearer token.
- A new Go binary, `mastermind-mcp`, that runs as a stdio MCP server
  spawned by the coding agent and translates MCP tool calls into
  `AdminService` RPCs.

The MCP server is a thin adapter: no persistence, no business logic.
Each tool call is exactly one gRPC call.

## 2. Non-Goals

- **Mutating tools beyond create.** `update_task_links`, `requeue_task`,
  and `delete_task` are deferred. The concrete ask is to add tasks.
- **Network-addressable MCP.** stdio only, spawned by the agent. SSE
  and streamable HTTP transports can be added later without rewriting
  the core handler package.
- **Per-agent identity / audit trail.** One shared `ADMIN_TOKEN`. If we
  later need to tell agents apart, we add a `created_by` field and
  per-agent tokens.
- **A separate admin user model.** `ADMIN_TOKEN` is a single shared
  credential; it does not interact with `ADMIN_USER` / `ADMIN_PASSWORD`
  which gate the HTMX UI.
- **Metrics on the MCP binary itself.** Short-lived per-session
  process; log-only observability.

## 3. Architecture

Third Go binary in the same module. stdio upstream, gRPC downstream.

```
┌──────────────┐  stdio (MCP / JSON-RPC 2.0)  ┌───────────────────┐
│ Coding agent │ ◄──────────────────────────► │  mastermind-mcp   │
│ (e.g. Claude │                              │  (Go stdio server)│
│  Code)       │                              └─────────┬─────────┘
└──────────────┘                                        │ gRPC :50051
                                                        │ Bearer ADMIN_TOKEN
                                              ┌─────────▼─────────┐
                                              │    mastermind     │
                                              │ TaskService (wor- │
                                              │ ker, existing) +  │
                                              │ AdminService (new)│
                                              └─────────┬─────────┘
                                                        │
                                                  ┌─────▼────┐
                                                  │ Postgres │
                                                  └──────────┘
```

### 3.1 Mastermind changes

- New `AdminService` served on the same gRPC listener as `TaskService`.
- Shared `grpc.UnaryInterceptor` learns per-method token policy: worker
  RPCs accept `WORKER_TOKEN`, admin RPCs accept `ADMIN_TOKEN`.
- One new required env var: `ADMIN_TOKEN`.

### 3.2 MCP binary (`cmd/mastermind-mcp`)

- Tiny `main()` — loads config, dials mastermind, hands off to
  `internal/mcpserver`.
- Exits fast on bad config or unreachable mastermind; the coding agent
  respawns the subprocess.
- Logs go to stderr. Stdout is exclusively the MCP framing channel.

### 3.3 Repository layout

```
cmd/
  mastermind/main.go
  worker/main.go
  mastermind-mcp/main.go       # NEW
internal/
  proto/
    tasks.proto                # EDIT — add AdminService + messages
    tasks.pb.go                # regenerated
    tasks_grpc.pb.go           # regenerated
  mastermind/
    grpcserver/
      server.go                # unchanged (TaskService)
      admin.go                 # NEW — AdminService over store.*
      admin_test.go            # NEW
  auth/
    bearer.go                  # EDIT — per-method token policy
    bearer_test.go             # EDIT / NEW
  mcpserver/                   # NEW — reusable MCP wiring
    server.go                  # builds mcp.Server, registers tools
    tools.go                   # create_task / get_task / list_tasks
    tools_test.go
    config.go                  # envconfig + flag overrides
Dockerfile.mcp                 # NEW
Makefile                       # EDIT — build-mcp, run-mcp targets
```

## 4. gRPC `AdminService`

Added to `internal/proto/tasks.proto`. Same package, same port, same
interceptor; separate token.

```proto
service AdminService {
  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);
  rpc GetTask   (GetTaskRequest)    returns (GetTaskResponse);
  rpc ListTasks (ListTasksRequest)  returns (ListTasksResponse);
}

message TaskDetail {
  string id                                        = 1;
  string name                                      = 2;
  bytes  payload                                   = 3;   // opaque JSON
  int32  priority                                  = 4;
  string status                                    = 5;   // pending|claimed|running|completed|failed
  google.protobuf.Timestamp scheduled_for          = 6;
  int32  attempt_count                             = 7;
  int32  max_attempts                              = 8;
  string claimed_by                                = 9;
  google.protobuf.Timestamp claimed_at             = 10;
  google.protobuf.Timestamp last_heartbeat_at      = 11;
  google.protobuf.Timestamp completed_at           = 12;
  string last_error                                = 13;
  string jira_url                                  = 14;
  string github_pr_url                             = 15;
  google.protobuf.Timestamp created_at             = 16;
  google.protobuf.Timestamp updated_at             = 17;
}

message CreateTaskRequest {
  string name                                      = 1;   // required
  bytes  payload                                   = 2;   // optional, default "{}"
  int32  priority                                  = 3;
  int32  max_attempts                              = 4;
  google.protobuf.Timestamp scheduled_for          = 5;
  string jira_url                                  = 6;
  string github_pr_url                             = 7;
}
message CreateTaskResponse { TaskDetail task = 1; }

message GetTaskRequest  { string id = 1; }
message GetTaskResponse { TaskDetail task = 1; }

message ListTasksRequest {
  string status                                    = 1;   // empty = all
  int32  limit                                     = 2;   // default 50, max 500
  int32  offset                                    = 3;
}
message ListTasksResponse {
  repeated TaskDetail items = 1;
  int32 total               = 2;
}
```

### 4.1 Implementation

`internal/mastermind/grpcserver/admin.go` delegates directly to the
existing `store` methods: `CreateTask`, `GetTask`, `ListTasks`. No new
queries are introduced. The handlers only translate proto <-> store
types and map errors.

### 4.2 Error mapping (server side)

| Condition                                    | gRPC code           |
|----------------------------------------------|---------------------|
| `name` empty                                 | `InvalidArgument`   |
| `payload` not parseable JSON                 | `InvalidArgument`   |
| `max_attempts` outside 1..50                 | `InvalidArgument`   |
| `limit` outside 1..500                       | `InvalidArgument`   |
| `status` filter not a known enum value       | `InvalidArgument`   |
| `id` not a valid UUID                        | `InvalidArgument`   |
| `GetTask` id not found                       | `NotFound`          |
| Auth missing / wrong token for the method    | `Unauthenticated`   |
| Store / DB error                             | `Internal`          |

### 4.3 Auth

`internal/auth/bearer.go` grows from a single-token check to a
per-method policy:

```go
// policy maps fully-qualified gRPC method name -> required token.
// Example:
//   "/elpulpo.tasks.v1.TaskService/ClaimTask"   -> workerToken
//   "/elpulpo.tasks.v1.AdminService/CreateTask" -> adminToken
func PerMethodInterceptor(policy map[string]string) grpc.UnaryServerInterceptor
```

- Missing method in the policy map → `Unimplemented`.
- Token mismatch → `Unauthenticated`.
- Mastermind `main.go` builds the policy from both `WORKER_TOKEN` and
  `ADMIN_TOKEN` at startup.

`ADMIN_TOKEN` becomes a required env var for mastermind. Startup fails
loudly if it is unset — same policy as `WORKER_TOKEN`.

## 5. MCP Tools

Three tools, one per `AdminService` RPC. snake_case names per MCP
convention. Input schemas are JSON Schema, generated by the official
SDK's typed-tools API from Go struct definitions.

### 5.1 `create_task`

Creates a task in `pending` state.

Input:
```json
{
  "name":          "string, required, 1-200 chars",
  "payload":       "object, optional, default {}",
  "priority":      "integer, optional, default 0",
  "max_attempts":  "integer, optional, default 3, range 1-50",
  "scheduled_for": "string (RFC3339), optional",
  "jira_url":      "string, optional",
  "github_pr_url": "string, optional"
}
```

Output: structured JSON (`TaskDetail` shape — see 5.4) + a short text
summary (`"Created task <id> (<name>)"`).

### 5.2 `get_task`

Input: `{ "id": "uuid" }`.
Output: `TaskDetail` JSON + one-line text summary.

### 5.3 `list_tasks`

Input:
```json
{
  "status": "one of: pending|claimed|running|completed|failed (optional)",
  "limit":  "integer, optional, default 50, max 500",
  "offset": "integer, optional, default 0"
}
```

Output: `{ "items": [TaskDetail...], "total": int }` + text summary
(`"<len(items)> of <total> tasks"`).

### 5.4 `TaskDetail` JSON shape

Same field set as the proto message, snake_case keys, RFC3339 UTC
timestamps. Nullable fields (e.g. `claimed_by` before a claim) are
omitted rather than serialized as empty strings.

### 5.5 Error handling

The MCP SDK distinguishes **protocol errors** (thrown) from **tool
errors** (`isError: true`, which the agent can see and retry). We
always produce tool errors — the MCP server itself should never die on
a bad gRPC response.

| gRPC status from mastermind      | MCP tool error message                                      |
|----------------------------------|-------------------------------------------------------------|
| `InvalidArgument`                | verbatim gRPC `message` (it is already user-facing)         |
| `NotFound` (get_task)            | `"task <id> not found"`                                     |
| `Unauthenticated`                | `"mastermind rejected admin token"` + log                   |
| `Unavailable` (connection lost mid-session) | `"mastermind unreachable at <addr>"`             |
| `Internal` / anything else       | `"internal error"` + log real cause to stderr               |

`Unavailable` at *startup* is a different case: it fails the initial
dial (section 7.1 step 2) and exits the process rather than turning
into a tool error. Only post-startup disconnects surface as tool
errors.

## 6. Configuration

### 6.1 `mastermind-mcp` binary

Env vars via envconfig, matching CLI flag overrides. Flag wins if both
are set.

| Var                | Flag              | Default | Required | Notes                                          |
|--------------------|-------------------|---------|:--------:|------------------------------------------------|
| `MASTERMIND_ADDR`  | `--addr`          | —       | ✅       | e.g. `localhost:50051`                         |
| `ADMIN_TOKEN`      | `--token`         | —       | ✅       | Shared bearer with mastermind                  |
| `MASTERMIND_TLS`   | `--tls`           | `false` |          | Use TLS instead of insecure gRPC credentials   |
| `DIAL_TIMEOUT`     | `--dial-timeout`  | `5s`    |          | Startup-time dial deadline                     |
| `LOG_LEVEL`        | `--log-level`     | `info`  |          | stderr only                                    |
| `LOG_FORMAT`       | `--log-format`    | `json`  |          | `json` or `text`                               |

### 6.2 Mastermind (new var)

| Var            | Default | Required | Notes                                  |
|----------------|---------|:--------:|----------------------------------------|
| `ADMIN_TOKEN`  | —       | ✅       | Loaded via `config.LoadMastermind`     |

## 7. Startup, Shutdown, and the Stdout Invariant

### 7.1 Startup (`cmd/mastermind-mcp/main.go`)

1. Load env + parse flags; validate required fields. Print a clear
   error to stderr and exit 1 on failure.
2. Dial mastermind with `DIAL_TIMEOUT`, `grpc.WithBlock()`. Failure →
   stderr log + exit 1. No retry loop; the agent respawns us.
3. Build `AdminServiceClient` + `internal/mcpserver`.
4. Serve MCP on `os.Stdin`/`os.Stdout`.
5. On stdin EOF, SIGTERM, or SIGINT: cancel context, close gRPC
   connection, exit 0.

### 7.2 The stdout invariant

`log/slog` writes to **stderr**, never stdout. Stdout is the MCP
framing channel; any stray `fmt.Println` or library log to stdout
corrupts the JSON-RPC stream. This is enforced in
`internal/mcpserver/server.go` by constructing the logger with an
explicit `os.Stderr` writer and never using the default `slog` logger.

## 8. Testing Strategy

Mirrors existing mastermind tests: `testcontainers-go` for Postgres,
`bufconn` for gRPC, no mocks where a real dependency is cheap.

### 8.1 `internal/mastermind/grpcserver/admin_test.go`

- Stand up the full gRPC server (both services) over `bufconn` with a
  real Postgres.
- `CreateTask`: happy path; empty name → `InvalidArgument`; bad JSON
  payload → `InvalidArgument`; new task visible via `GetTask`.
- `GetTask`: happy path; unknown UUID → `NotFound`; malformed UUID →
  `InvalidArgument`.
- `ListTasks`: no filter; with `status` filter; `limit`/`offset`;
  `total` correct; bad `status` → `InvalidArgument`.
- Auth matrix: worker token on admin RPC → `Unauthenticated`; worker
  token on worker RPC still works; admin token on worker RPC →
  `Unauthenticated`; missing token → `Unauthenticated`.

### 8.2 `internal/auth/bearer_test.go`

Per-method policy resolution, token-mismatch codes, unknown-method
handling.

### 8.3 `internal/mcpserver/tools_test.go`

- Build the MCP server wired to an in-process `AdminService` (bufconn)
  with a real store + Postgres.
- Use the official SDK's in-memory client/server pair to drive tool
  calls without stdio plumbing.
- For each tool: input-schema validation (missing required fields),
  happy-path call, gRPC error → MCP tool error translation (e.g. close
  the gRPC conn mid-test to force `Unavailable`).
- Assert structured JSON output matches `TaskDetail`.

### 8.4 `cmd/mastermind-mcp` smoke test

Launch the binary with stdin/stdout pipes against a real mastermind
running on bufconn-equivalent loopback; send `initialize` +
`tools/list`; assert the three tool names come back. Only test that
exercises the actual stdio transport.

### 8.5 What we don't test

- The generated `.pb.go` files.
- Re-testing `store.*` (already covered in
  `internal/mastermind/store/*_test.go`); `admin.go` is thin enough
  that its own tests cover the delegation.

## 9. Deployment

- `Dockerfile.mcp` (multi-stage, distroless final stage). Same pattern
  as `Dockerfile.mastermind` and `Dockerfile.worker`.
- Makefile additions:
  - `make build-mcp` — builds the binary.
  - `make run-mcp` — runs it locally against a local mastermind for
    manual smoke-testing (e.g., piping a hand-rolled JSON-RPC request).
  - Default `make build` target includes `mastermind-mcp`.
- `make proto` regenerates `tasks.pb.go` and `tasks_grpc.pb.go`.

## 10. Agent Integration

Example `.mcp.json` for Claude Code (added to the README):

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

The tool schemas are self-describing via `tools/list`; no additional
end-user docs are required.

## 11. Security Notes

- `ADMIN_TOKEN` is a secret. The README warns against checking it
  into `.mcp.json` under version control and suggests sourcing it
  from the shell environment or a secrets manager.
- Token-based boundary only; no IP allowlist. Same posture as
  `WORKER_TOKEN`.
- The stdio server never accepts network input directly — it only
  speaks to its parent process. Attack surface is the gRPC client
  call site, not the MCP stdio layer.

## 12. Dependencies (proposed)

- `github.com/modelcontextprotocol/go-sdk` — official MCP Go SDK.
- Reuses: `google.golang.org/grpc`, `google.golang.org/protobuf`,
  `github.com/kelseyhightower/envconfig`, `github.com/google/uuid`,
  `log/slog`.

No changes to Postgres driver, migrations, or the worker binary.

## 13. Open Questions (deferred)

- **Write tools beyond create** (`update_task_links`, `requeue_task`,
  `delete_task`). Out of scope this iteration; add when a concrete
  need appears.
- **Network-addressable MCP transport** (SSE / streamable HTTP).
  Deferred. Most of `internal/mcpserver` is reusable; only
  `cmd/mastermind-mcp/main.go` would change.
- **Admin API versioning.** `AdminService` is v1; no breaking-change
  policy yet. If the service grows, split into
  `admin/v1/admin.proto` on the first breaking change.
- **Per-agent identity / audit.** Shared `ADMIN_TOKEN` does not tell
  us which agent or session created a task. If this becomes important,
  add an optional `created_by` field and per-agent tokens.
