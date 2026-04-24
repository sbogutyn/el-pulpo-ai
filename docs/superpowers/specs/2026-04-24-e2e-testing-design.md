# End-to-End Testing — Design

**Date:** 2026-04-24
**Status:** Approved for planning (autonomous mode — see README of the PR)
**Language / Runtime:** Go

## 1. Goal

Give el-pulpo a production-like end-to-end test suite that can be brought up
locally with a single command and covers every externally-observable feature
of the system:

- The `mastermind` gRPC `TaskService` (worker-facing) and `AdminService`
  (tooling-facing), authenticated with distinct bearer tokens.
- The `mastermind` admin HTTP UI (HTMX + server-rendered templates) behind
  HTTP basic auth, plus the `/healthz`, `/readyz`, `/metrics` unauthenticated
  probes.
- The `worker` binary's local MCP HTTP endpoint (`/mcp`) and `/healthz` probe,
  including all six tools the coding-agent sees.
- The `mastermind-mcp` stdio binary and its three admin tools.
- The reaper loop (lease expiration → retry / terminal failure).
- The retry-with-linear-backoff path driven from the worker side.
- Prometheus metrics wiring.
- Migrations applied on mastermind startup.

The unit-and-integration suite (`go test ./...`, which uses
`testcontainers-go`) covers the internal wiring of each package. This new
layer binds the *binaries* together exactly the way a production deployment
would — same Dockerfiles, same env vars, same networking — so the tests fail
for the same reasons production would.

## 2. Non-Goals

- **Load / performance benchmarks.** We assert correctness, not throughput.
- **Chaos testing** (killing a container mid-task, network partitions). Out of
  scope for this iteration; the reaper test covers the one failure mode we
  actually depend on.
- **CI integration.** The suite is optional (`//go:build e2e`) and excluded
  from the default `go test ./...` run. Wiring it into GitHub Actions is a
  follow-up.
- **Cross-platform verification.** The harness is developed and verified on
  macOS/darwin-arm64 with Docker Desktop. Linux should Just Work; Windows is
  out of scope.
- **Real coding-agent integration.** The MCP endpoints are exercised by a
  Go-based MCP client (the official SDK's in-process client speaking to the
  server's HTTP/stdio transport). No real LLM is spawned.

## 3. Approach

### 3.1 Options considered

| # | Approach | Pros | Cons |
|---|----------|------|------|
| A | Extend the existing `internal/e2e` testcontainers-based tests with more in-process coverage (bufconn + embedded binaries). | Fast; no Docker required; already the pattern for unit tests. | Doesn't exercise the Dockerfiles, env-var loading, TCP listeners, or the real HTTP/gRPC wire. "Production-like" in name only. |
| B | Docker-Compose-based stack + Go test suite that drives it end-to-end. | Tests what we actually deploy; same containers, same ports, same auth; easy to reproduce locally; no new heavy dependencies. | Slower first run (image build); requires Docker on the dev machine. |
| C | Kubernetes-in-docker (kind / k3d) with manifests. | Closest to a real prod target. | Huge overkill for two binaries + Postgres; new deploy artifacts to maintain; slow. |

**Recommendation: Option B.** We already ship three Dockerfiles the tests
should exercise; docker-compose is the minimum ceremony to wire them up; and
the dev-UX ("one `make` target, everything runs") is exactly what was asked
for.

### 3.2 High-level architecture

```
┌───────────────────────────────────────────────────────────────────────┐
│ Go E2E test binary  (build tag `e2e`)                                 │
│   - TestMain: bring stack up / tear down                              │
│   - Tests: drive gRPC, HTTP, and MCP clients against the stack        │
└─────────┬─────────────────────────────────────────────────────────────┘
          │ docker compose up --build --wait
          ▼
┌──────────────────────────────────────────────────────────────────────┐
│ docker-compose.e2e.yml                                               │
│                                                                      │
│   postgres (15432 → 5432)                                            │
│         ▲                                                            │
│         │                                                            │
│   mastermind (15051 → 50051 gRPC, 18080 → 8080 HTTP)                 │
│         ▲                                                            │
│         │ gRPC + token                                               │
│   worker   (17777 → 7777 MCP HTTP)                                   │
└──────────────────────────────────────────────────────────────────────┘
          ▲                       ▲                      ▲
          │ gRPC                  │ HTTP+MCP             │ stdio
          │ (ports 15051/18080)   │ (17777)              │
          │                       │                      │
┌─────────┴──┐        ┌───────────┴─────┐    ┌───────────┴──────────┐
│ admin/worker│        │ worker MCP SDK  │    │ mastermind-mcp stdio │
│ gRPC client │        │ in-process      │    │ subprocess launched  │
│ (Go)        │        │ client          │    │ by the test          │
└─────────────┘        └─────────────────┘    └──────────────────────┘
```

See the flow diagram in §10.

All listeners are published on `127.0.0.1` with deliberately non-default
ports so the suite does not collide with a developer's `make dev-up` /
`make run-mastermind` / `make run-worker` session.

### 3.3 Components

| Component | Role |
|-----------|------|
| `docker-compose.e2e.yml` | Builds the three binaries from the existing Dockerfiles and wires them to a single Postgres, with known env vars and known ports. |
| `e2e/stack.go` | Thin Go wrapper over the `docker compose` CLI: `Up`, `Down`, `WaitReady`. Stores ports and tokens in a `Stack` struct handed to every test. |
| `e2e/admin_grpc_test.go` | `AdminService` RPC coverage. |
| `e2e/worker_grpc_test.go` | `TaskService` RPC coverage (claim, heartbeat, report, progress, log), plus the auth matrix. |
| `e2e/admin_http_test.go` | Admin UI coverage: basic auth, CRUD, fragment, links, requeue, /, redirects. |
| `e2e/probes_test.go` | `/healthz`, `/readyz`, `/metrics` endpoints. |
| `e2e/worker_mcp_test.go` | Worker MCP HTTP endpoint, all six tools, using `mcp.NewClient` against `mcp.StreamableClientTransport`. |
| `e2e/mastermind_mcp_test.go` | `mastermind-mcp` stdio binary driven via `mcp.CommandTransport` (the SDK's subprocess transport). |
| `e2e/reaper_test.go` | Reaper-driven lease expiration. Uses a second compose profile with `VISIBILITY_TIMEOUT=2s` / `REAPER_INTERVAL=500ms` — see §5.3. |
| `e2e/journey_test.go` | The headline flow: admin creates → worker claims → worker progresses + logs → worker completes → admin sees completed → metrics confirm. |

### 3.4 Ports, tokens, credentials

Fixed at stack boot so tests don't need discovery:

| Service | Host port | In-container port | Env var |
|---------|-----------|-------------------|---------|
| Postgres | 15432 | 5432 | — |
| Mastermind gRPC | 15051 | 50051 | `GRPC_LISTEN_ADDR=:50051` |
| Mastermind HTTP | 18080 | 8080 | `HTTP_LISTEN_ADDR=:8080` |
| Worker MCP HTTP | 17777 | 7777 | `WORKER_MCP_LISTEN_ADDR=0.0.0.0:7777` |

| Secret | Value (test-only) | Notes |
|--------|-------------------|-------|
| `WORKER_TOKEN` | `e2e-worker-token` | Shared by mastermind + worker |
| `ADMIN_TOKEN` | `e2e-admin-token` | Shared by mastermind + mastermind-mcp |
| `ADMIN_USER` / `ADMIN_PASSWORD` | `e2e` / `e2e` | HTTP basic for the admin UI |
| Postgres DSN | `postgres://pulpo:pulpo@postgres:5432/pulpo?sslmode=disable` | Inside the compose network |

The worker binds its MCP listener to `0.0.0.0:7777` instead of the default
`127.0.0.1:7777` *only* inside the e2e compose network; the container is
still not reachable from outside because Docker publishes the port back to
`127.0.0.1:17777`. This is the minimum deviation from prod required for the
test client to reach the worker through Docker's NAT layer. The
DNS-rebinding protection inside the MCP SDK is still effective because the
client always addresses the endpoint as `127.0.0.1:17777` (loopback
short-circuits the SDK's host-header check).

### 3.5 Stack lifecycle

- `TestMain` runs `docker compose -f docker-compose.e2e.yml up -d --build
  --wait --wait-timeout 180`. The `--wait` flag requires a `healthcheck:`
  block on every service, which we provide.
- If `E2E_KEEP=1`, `TestMain` skips the teardown on exit so the developer
  can poke the stack. Otherwise it runs `docker compose ... down -v`.
- Concurrent test runs on one machine are out of scope — everything uses
  fixed ports. A `-failfast`-style behavior falls out naturally because the
  first `Up` call will fail if the ports are busy.

### 3.6 Make target

A new `make test-e2e` target:

```make
test-e2e:
	go test -tags=e2e -timeout=15m ./e2e/...
```

Plus `make test-e2e-keep` that sets `E2E_KEEP=1` for interactive debugging.

## 4. Test Coverage Matrix

Every feature the system exposes to an external caller is exercised exactly
once in the e2e suite, and only once — unit tests cover internal permutations.

### 4.1 Mastermind — gRPC `TaskService` (worker-facing)

| Feature | Test |
|---------|------|
| ClaimTask empty queue → `NotFound` | `TestWorkerGRPC_ClaimEmpty` |
| ClaimTask returns next task | `TestWorkerGRPC_ClaimReturnsTask` |
| Heartbeat refreshes lease | `TestWorkerGRPC_Heartbeat` |
| Heartbeat on foreign task → `FailedPrecondition` | `TestWorkerGRPC_HeartbeatForeign` |
| UpdateProgress sets note | `TestWorkerGRPC_UpdateProgress` |
| AppendLog returns id, shows in logs | `TestWorkerGRPC_AppendLog` |
| ReportResult success transitions to `completed` | `TestWorkerGRPC_ReportSuccess` |
| ReportResult failure with attempts left → retry | `TestWorkerGRPC_ReportRetry` |
| ReportResult failure with exhausted attempts → `failed` | `TestWorkerGRPC_ReportTerminal` |

### 4.2 Mastermind — gRPC `AdminService`

| Feature | Test |
|---------|------|
| CreateTask happy path | `TestAdminGRPC_CreateHappy` |
| CreateTask empty name → `InvalidArgument` | `TestAdminGRPC_CreateInvalidName` |
| CreateTask bad JSON payload → `InvalidArgument` | `TestAdminGRPC_CreateInvalidPayload` |
| GetTask happy + not-found | `TestAdminGRPC_GetTask` |
| ListTasks unfiltered, total correct | `TestAdminGRPC_ListAll` |
| ListTasks filtered by status | `TestAdminGRPC_ListFiltered` |
| ListTasks bad status → `InvalidArgument` | `TestAdminGRPC_ListInvalidStatus` |
| ListTaskLogs returns appended lines | `TestAdminGRPC_ListLogs` |

### 4.3 Auth matrix (gRPC)

| Scenario | Expected |
|----------|----------|
| No token | `Unauthenticated` |
| Worker token on admin method | `Unauthenticated` |
| Admin token on worker method | `Unauthenticated` |
| Wrong token on any method | `Unauthenticated` |

Combined into `TestAuthMatrix_GRPC`.

### 4.4 Admin UI (HTTP + HTMX)

| Feature | Test |
|---------|------|
| `/` redirects to `/tasks` | `TestHTTP_RootRedirect` |
| GET `/tasks` — unauthenticated → 401 | `TestHTTP_TasksRequiresAuth` |
| GET `/tasks` with basic auth → 200 HTML | `TestHTTP_TasksListOK` |
| GET `/tasks/fragment` returns just the table | `TestHTTP_TasksFragment` |
| GET `/tasks/new` renders form | `TestHTTP_NewForm` |
| POST `/tasks` creates and redirects | `TestHTTP_CreateTask` |
| GET `/tasks/{id}` detail | `TestHTTP_TaskDetail` |
| GET `/tasks/{id}/edit` renders edit form | `TestHTTP_EditForm` |
| POST `/tasks/{id}` updates pending task | `TestHTTP_UpdateTask` |
| POST `/tasks/{id}/links` attaches refs to a terminal task | `TestHTTP_UpdateLinks` |
| POST `/tasks/{id}/requeue` requeues terminal task | `TestHTTP_Requeue` |
| POST `/tasks/{id}/delete` deletes terminal task | `TestHTTP_Delete` |
| GET `/static/htmx.min.js` requires auth and serves | `TestHTTP_StaticAuth` |

### 4.5 Probes + metrics

| Feature | Test |
|---------|------|
| `/healthz` → 200 "ok" without auth | `TestProbes_Healthz` |
| `/readyz` → 200 once DB is up | `TestProbes_Readyz` |
| `/metrics` exposes `tasks_claimed_total`, `tasks_completed_total`, `tasks_pending`, `claim_duration_seconds` | `TestProbes_Metrics` |

### 4.6 Worker MCP (HTTP)

| Feature | Test |
|---------|------|
| Worker `/healthz` 200 | `TestWorkerMCP_Healthz` |
| `tools/list` returns 6 tools with the expected names | `TestWorkerMCP_ToolsList` |
| `claim_next_task` on empty queue → tool error | `TestWorkerMCP_ClaimEmpty` |
| `claim_next_task` happy path returns TaskView | `TestWorkerMCP_ClaimHappy` |
| `get_current_task` idle → tool error, post-claim → current task | `TestWorkerMCP_GetCurrent` |
| `update_progress` reflects in admin detail | `TestWorkerMCP_UpdateProgress` |
| `append_log` increments log count | `TestWorkerMCP_AppendLog` |
| `complete_task` transitions task to `completed` | `TestWorkerMCP_Complete` |
| `fail_task` retries until exhausted → `failed` | `TestWorkerMCP_Fail` |

### 4.7 Mastermind-MCP (stdio)

Launched as an actual subprocess with
`mcp.NewClient(...).Connect(ctx, mcp.CommandTransport{Cmd: exec.Command("mastermind-mcp")}, nil)`.
The binary is pulled out of the built `mastermind-mcp` image via
`docker cp` once at stack-up time and cached at
`e2e/bin/mastermind-mcp`.

| Feature | Test |
|---------|------|
| `tools/list` returns 3 tools | `TestMMCP_ToolsList` |
| `create_task` happy path | `TestMMCP_Create` |
| `create_task` invalid payload → tool error | `TestMMCP_CreateInvalid` |
| `get_task` happy + not-found | `TestMMCP_GetTask` |
| `list_tasks` with status filter | `TestMMCP_List` |
| Auth failure maps to `"mastermind rejected admin token"` | `TestMMCP_AuthFailure` — spawn binary with a bad token |

### 4.8 Reaper + retry

The primary mastermind container runs with tight reaper settings —
`VISIBILITY_TIMEOUT=5s`, `REAPER_INTERVAL=1s` — so the test doesn't have to
sit for 30s. Worker auto-heartbeats default to 1s (see §7), which stays
comfortably inside the visibility window for every other test, and no test
holds a claim *without* heartbeating except the reaper test itself.

This is simpler than a second mastermind profile and the original design's
concern (that tight reaper settings would step on normal tests) does not
actually bite: the worker container only claims when the MCP
`claim_next_task` tool is invoked — there is no background claim loop in
`cmd/worker` — so the set of claims in flight at any moment is fully under
the test's control.

| Feature | Test |
|---------|------|
| Claim + stop heartbeating → reaper requeues | `TestReaper_Requeue` |
| Exhausted attempts + expired lease → `failed` | `TestReaper_Terminal` |

### 4.9 Integration journey

`TestJourney_EndToEnd`: the single most important test.

```
AdminService.CreateTask("demo", {"k":"v"})
  → poll list_tasks until status = pending
  → WorkerMCP.claim_next_task returns that exact id
  → WorkerMCP.update_progress("starting")
  → GET /tasks/{id} (admin UI) shows progress_note = "starting"
  → WorkerMCP.append_log("step 1") / append_log("step 2")
  → AdminService.ListTaskLogs returns both lines in order
  → WorkerMCP.complete_task
  → AdminService.GetTask shows status = completed
  → /metrics shows tasks_completed_total has incremented by >= 1
```

This proves the three binaries plus the admin UI all agree about one task's
lifecycle.

## 5. Open Design Decisions (resolved inline)

### 5.1 Why Go tests instead of shell + curl?

Because the system's interface is already Go: `pb.AdminServiceClient`,
`mcp.Client`, `http.Client`. Reusing the real clients catches wire-format
bugs the way prod would see them, and the suite can share helpers with the
rest of the codebase (e.g. the `auth` package's `BearerCredentials`).

### 5.2 Why not testcontainers-compose?

The project already vendors `testcontainers-go` for its unit tests, and
testcontainers ships a compose module. We chose the plain `docker compose`
CLI instead because:

- The compose file is an artifact worth inspecting and running manually.
- `docker compose up --wait` gives us healthcheck-gated readiness for free.
- We avoid pulling a second level of abstraction between the test binary and
  Docker. The `os/exec` wrapper is ~60 lines.

### 5.3 Why a second mastermind for the reaper tests?

`VISIBILITY_TIMEOUT` is a mastermind-level config; changing it requires a
restart. We don't want the reaper test's aggressive settings to destabilize
the other tests' longer-running claims (e.g. the worker MCP tests that
deliberately hold a task idle while asserting state). A second service with
its own port + DB schema keeps the two regimes isolated without a complex
"reset the primary" helper.

### 5.4 Why `//go:build e2e`?

`go test ./...` must stay fast and Docker-free. The build tag partitions the
slow / Docker-coupled tests so running the unit suite in CI (or a dev loop)
doesn't require the compose stack to be up.

### 5.5 Why not log-scrape?

We assert observable behavior through the same APIs a real caller uses:
gRPC responses, HTTP status + body, MCP tool results, Prometheus metrics.
Log scraping is brittle and deferred.

## 6. Repository Additions

```
docker-compose.e2e.yml                            # NEW
e2e/                                              # NEW top-level package
  stack.go                                        # compose wrapper
  stack_test.go
  testmain_test.go                                # TestMain: up/down
  clients.go                                      # gRPC + HTTP + MCP helpers
  admin_grpc_test.go
  worker_grpc_test.go
  admin_http_test.go
  probes_test.go
  worker_mcp_test.go
  mastermind_mcp_test.go
  reaper_test.go
  journey_test.go
  README.md                                       # one-pager: how to run
docs/superpowers/specs/2026-04-24-e2e-testing-design.md    # THIS DOC
docs/superpowers/plans/2026-04-24-e2e-testing.md           # Implementation plan (follow-up)
Makefile                                          # EDIT — add test-e2e / test-e2e-keep
README.md                                         # EDIT — link to e2e/README.md
```

No changes to the production binaries. The only source edit outside of
`e2e/` is the Makefile; the only doc edits are the README pointer and this
doc tree.

## 7. Configuration Summary

The full env map the compose file applies to each service:

**`postgres`** (healthcheck: `pg_isready -U pulpo`)
```
POSTGRES_USER=pulpo
POSTGRES_PASSWORD=pulpo
POSTGRES_DB=pulpo
```

**`mastermind`** (healthcheck: `wget -qO- http://127.0.0.1:8080/readyz`)
```
DATABASE_URL=postgres://pulpo:pulpo@postgres:5432/pulpo?sslmode=disable
GRPC_LISTEN_ADDR=:50051
HTTP_LISTEN_ADDR=:8080
WORKER_TOKEN=e2e-worker-token
ADMIN_TOKEN=e2e-admin-token
ADMIN_USER=e2e
ADMIN_PASSWORD=e2e
VISIBILITY_TIMEOUT=5s
REAPER_INTERVAL=1s
LOG_LEVEL=info
LOG_FORMAT=text
```

**`worker`** (healthcheck: `wget -qO- http://127.0.0.1:7777/healthz`)
```
MASTERMIND_ADDR=mastermind:50051
WORKER_TOKEN=e2e-worker-token
WORKER_MCP_LISTEN_ADDR=0.0.0.0:7777
HEARTBEAT_INTERVAL=1s
LOG_LEVEL=info
LOG_FORMAT=text
```

`mastermind-mcp` is *not* a compose service — it runs as a stdio subprocess
spawned by the test. It reads `MASTERMIND_ADDR=127.0.0.1:15051`,
`ADMIN_TOKEN=e2e-admin-token` from the test's environment.

## 8. Error Model / Observability

Every test asserts at least one observable, including:

- gRPC response status code and message substring where relevant.
- HTTP response status, `Content-Type`, and a short body substring (not the
  whole page — templates change).
- MCP tool result `IsError` flag and the text-content message.
- Metrics: the specific counter name and a numeric comparison (`>=` or `>`),
  never an exact equality (Prometheus counters may jitter across retries).

When a test fails, `stack.go` calls `docker compose logs --no-color --tail=200`
for each service and prints the output through `t.Log`. The developer does
not need to find container logs manually.

## 9. Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Image build is slow on first run | Use Docker BuildKit (on by default in 28.x), cache the Go module + build cache via the existing Dockerfile mounts, and document a one-time `make test-e2e` warm-up in the README. |
| Ports collide with `make dev-up` | Dedicated non-default ports (`15432`, `15051`, `18080`, `17777`). |
| Reaper test races are flaky under load | 500ms reaper tick with a 2s visibility timeout gives >3x margin on typical CI. If flakiness appears, relax the tick (1s) before widening the visibility. |
| `mastermind-mcp` stdio subprocess leaks when a test panics | `t.Cleanup` calls `client.Close()` and `cmd.Process.Kill()`; the compose stack tears down in `TestMain` so any orphan processes die with the harness shell. |
| Docker not installed on the developer machine | `TestMain` exits early with a `t.Skip` when `docker` is missing so `go test -tags=e2e ./e2e/...` degrades gracefully. |

## 10. Flow Diagram (Mermaid)

```mermaid
sequenceDiagram
    autonumber
    participant T as E2E test (Go)
    participant DC as docker compose
    participant PG as Postgres
    participant M as mastermind (container)
    participant W as worker (container)
    participant MM as mastermind-mcp (stdio subprocess)

    T->>DC: up -d --build --wait
    DC->>PG: start
    DC->>M: start (waits for PG healthy)
    DC->>W: start (waits for M healthy)
    DC-->>T: ready

    Note over T,M: Admin path
    T->>M: AdminService.CreateTask("demo", {...})
    M->>PG: INSERT tasks
    M-->>T: TaskDetail(status=pending, id=X)

    Note over T,W: Worker MCP path
    T->>W: MCP POST /mcp call_tool(claim_next_task)
    W->>M: gRPC ClaimTask
    M->>PG: UPDATE SKIP LOCKED
    M-->>W: Task{id=X,...}
    W-->>T: TaskView{id=X}
    loop heartbeat every 1s
        W->>M: gRPC Heartbeat
    end
    T->>W: MCP call_tool(update_progress,"starting")
    W->>M: gRPC UpdateProgress
    T->>W: MCP call_tool(append_log,"step 1")
    W->>M: gRPC AppendLog

    Note over T,M: Admin observes
    T->>M: HTTP GET /tasks/{id}  (basic auth)
    M-->>T: HTML + progress_note="starting"

    T->>W: MCP call_tool(complete_task)
    W->>M: gRPC ReportResult(success)
    M->>PG: UPDATE status=completed
    W-->>T: "task completed"

    Note over T,MM: Admin-MCP path
    T->>MM: spawn mastermind-mcp
    MM->>M: gRPC AdminService.ListTasks(status="completed")
    M-->>MM: {items: [...X...], total: N}
    MM-->>T: list result

    T->>M: HTTP GET /metrics
    M-->>T: prometheus text incl. tasks_completed_total

    T->>DC: down -v
```

## 11. Transition to Implementation

Next step is to write the implementation plan as
`docs/superpowers/plans/2026-04-24-e2e-testing.md` using the writing-plans
skill, then execute it autonomously and open a PR.

Because this project is running in autonomous mode (user directive:
"work autonomously and at the end create a PR"), the design doc is not
gated on further user review; proceed straight to planning and execution.
