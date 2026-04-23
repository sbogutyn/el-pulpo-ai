# Mastermind / Worker Task Queue — Design

**Date:** 2026-04-23
**Status:** Approved for planning
**Language / Runtime:** Go

## 1. Goal

Build a production-bound distributed task queue with two components:

- A **mastermind** server (single instance) that owns the task database and exposes a gRPC API for workers plus an HTMX-driven admin UI for defining tasks.
- A **worker** binary (horizontally scalable) that claims tasks from the mastermind, performs work, and reports results.

For this iteration, workers do not perform real work: they sleep for one minute per task and then mark it complete. The goal is a production-quality scaffold — communication, persistence, failure handling, observability — onto which real task types can be added later.

## 2. Non-Goals

- Real task handlers. Workers only fake work with a one-minute sleep.
- Multi-tenant concerns, per-tenant isolation, or user management beyond a single admin account.
- High-availability / leader-election for the mastermind. It is a single instance by design.
- Worker auto-scaling. Operators run as many worker processes as they want.
- Task chaining, DAGs, or dependencies. The queue is flat.

## 3. Architecture

Two Go binaries in a single module. Single Postgres instance owned by the mastermind.

```
          ┌──────────────┐
          │  Admin UI    │   (HTMX in a browser)
          └──────┬───────┘
                 │ HTTP :8080 (HTTP basic auth)
          ┌──────▼──────────┐        ┌────────────┐
          │    mastermind   │◄──────►│ PostgreSQL │
          │   (single node) │        └────────────┘
          └──────┬──────────┘
                 │ gRPC :50051 (bearer-token auth)
        ┌────────┼────────┐
        ▼        ▼        ▼
    ┌───────┐┌───────┐┌───────┐
    │worker ││worker ││worker │   (N instances)
    └───────┘└───────┘└───────┘
```

### 3.1 Mastermind responsibilities

- Owns all database access.
- Exposes a gRPC `TaskService` with three RPCs: `ClaimTask`, `Heartbeat`, `ReportResult`.
- Exposes an HTTP server hosting the admin UI, `/healthz`, `/readyz`, and `/metrics`.
- Runs a background **reaper** goroutine that reclaims tasks whose leases have expired.
- Runs DB migrations on startup before accepting traffic.

### 3.2 Worker responsibilities

- Generates a UUID `worker_id` at startup.
- Runs a single claim loop: call `ClaimTask`, and if a task is returned, execute it while a heartbeat goroutine keeps the lease alive; then call `ReportResult`.
- On shutdown, stop heartbeating in-flight work and exit. The reaper will reclaim the task.

### 3.3 Repository layout

```
cmd/
  mastermind/main.go        # wires config + starts gRPC and HTTP servers
  worker/main.go            # wires config + starts claim loop
internal/
  proto/                    # .proto files + generated code
  mastermind/
    grpcserver/             # gRPC handlers (ClaimTask, Heartbeat, ReportResult)
    httpserver/             # HTTP handlers for admin UI
    store/                  # Postgres access (claim query, CRUD)
    reaper/                 # background reclaimer goroutine
    templates/              # html/template files
  worker/
    runner/                 # claim loop, heartbeat goroutine, fake-work
  auth/                     # bearer-token interceptor + HTTP basic auth middleware
  config/                   # env-var config loading
migrations/                 # golang-migrate SQL files
```

## 4. Data Model

Single table `tasks` in Postgres.

```sql
CREATE TYPE task_status AS ENUM (
    'pending', 'claimed', 'running', 'completed', 'failed'
);

CREATE TABLE tasks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT NOT NULL,
    payload           JSONB NOT NULL DEFAULT '{}'::jsonb,
    priority          INTEGER NOT NULL DEFAULT 0,
    status            task_status NOT NULL DEFAULT 'pending',
    scheduled_for     TIMESTAMPTZ,                 -- NULL = eligible immediately
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    max_attempts      INTEGER NOT NULL DEFAULT 3,
    claimed_by        TEXT,                        -- worker_id; NULL when unclaimed
    claimed_at        TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tasks_claimable
    ON tasks (priority DESC, created_at ASC)
    WHERE status = 'pending';

CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'running');
```

### 4.1 State machine

```
pending ──claim──► claimed ──first-heartbeat──► running ──success──► completed
   ▲                  │                            │
   │                  │ lease expired              │ failure
   │                  ▼                            ▼
   └── attempts<max ──── retry (with backoff)    failed (if attempts exhausted)
```

- `pending` → `claimed`: on a successful `ClaimTask`.
- `claimed` → `running`: on the first `Heartbeat` for this claim. (Distinguishes "picked up but not yet started" from "actively working".)
- `running` → `completed`: on `ReportResult{success=true}`.
- `running`/`claimed` → `pending` or `failed`: on `ReportResult{success=false}` (retry if attempts remain, else `failed`).
- `running`/`claimed` → `pending` or `failed`: by the reaper when `now() - last_heartbeat_at > VISIBILITY_TIMEOUT` (retry if attempts remain, else `failed`).

### 4.2 Retry and backoff

- `max_attempts` defaults to 3, editable per task.
- `attempt_count` increments each time a task is claimed.
- On retry (whether by `ReportResult` failure or reaper reclaim), the row is reset to `pending` with `scheduled_for = now() + attempt_count * 30s` so the queue honors a simple linear backoff.
- On terminal failure, `last_error` is populated.

## 5. gRPC API

Defined in `internal/proto/tasks.proto`. All RPCs require `authorization: Bearer <WORKER_TOKEN>` in metadata; a unary interceptor enforces this on the server.

```proto
syntax = "proto3";
package elpulpo.tasks.v1;

service TaskService {
  rpc ClaimTask(ClaimTaskRequest) returns (ClaimTaskResponse);
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
  rpc ReportResult(ReportResultRequest) returns (ReportResultResponse);
}

message Task {
  string id = 1;
  string name = 2;
  bytes  payload = 3;   // opaque JSON
}

message ClaimTaskRequest  { string worker_id = 1; }
message ClaimTaskResponse {
  Task task = 1;        // absent => no work available (server returns NotFound)
}

message HeartbeatRequest  { string worker_id = 1; string task_id = 2; }
message HeartbeatResponse {}

message ReportResultRequest {
  string worker_id = 1;
  string task_id = 2;
  bool   success = 3;
  string error_message = 4;
}
message ReportResultResponse {}
```

### 5.1 Claim query (canonical)

Runs inside a single transaction in `ClaimTask`:

```sql
UPDATE tasks
SET status            = 'claimed',
    claimed_by        = $1,
    claimed_at        = now(),
    last_heartbeat_at = now(),
    updated_at        = now(),
    attempt_count     = attempt_count + 1
WHERE id = (
    SELECT id FROM tasks
    WHERE status = 'pending'
      AND (scheduled_for IS NULL OR scheduled_for <= now())
    ORDER BY priority DESC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, name, payload;
```

`SKIP LOCKED` guarantees that concurrent `ClaimTask` calls each receive a distinct row without blocking each other. If the inner `SELECT` returns no rows, the RPC responds with gRPC status `NotFound`.

### 5.2 Heartbeat semantics

`Heartbeat` updates `last_heartbeat_at = now()` and sets `status = 'running'` if the row is currently `claimed`. The handler validates that `claimed_by = request.worker_id` and `status IN ('claimed', 'running')`; otherwise it returns `FailedPrecondition` (the worker should drop the task — the reaper has already reclaimed it).

### 5.3 ReportResult semantics

- `success = true`: set `status = 'completed'`, `completed_at = now()`.
- `success = false` and `attempt_count < max_attempts`: reset to `pending`, set `scheduled_for = now() + attempt_count * 30s`, record `last_error`.
- `success = false` and `attempt_count >= max_attempts`: set `status = 'failed'`, record `last_error`.

The handler validates ownership the same way `Heartbeat` does.

## 6. Reaper

Background goroutine in the mastermind, ticking every `REAPER_INTERVAL` (default 10s). Executes:

```sql
-- For each row in claimed/running past its visibility window:
--   * if attempts < max_attempts: reset to pending with backoff
--   * else: mark failed with last_error = 'lease expired'
```

The reaper runs as one `UPDATE` with a CTE so it is atomic and single-pass. Emits `tasks_reaped_total` metric.

## 7. Worker Runtime

One claim loop per worker process (concurrency = 1).

```go
for ctx.Err() == nil {
    task, err := client.ClaimTask(ctx, &ClaimTaskRequest{WorkerID: id})
    if status.Code(err) == codes.NotFound {
        sleepWithJitter(ctx, pollInterval)
        continue
    }
    if err != nil {
        sleepWithJitter(ctx, backoff)
        continue
    }

    taskCtx, cancel := context.WithCancel(ctx)
    go heartbeatLoop(taskCtx, client, id, task.Id)

    workErr := fakeWork(taskCtx)  // time.Sleep(1m), respects context

    cancel()

    _, _ = client.ReportResult(ctx, &ReportResultRequest{
        WorkerID: id, TaskId: task.Id,
        Success:      workErr == nil,
        ErrorMessage: errString(workErr),
    })
}
```

Settings (all env-configurable):

- `POLL_INTERVAL` = 2s with ±25% jitter.
- `HEARTBEAT_INTERVAL` = 10s.
- `VISIBILITY_TIMEOUT` (server-side) = 30s. Chosen so the reaper tolerates two missed heartbeats.

On SIGTERM: cancel the top-level context, stop heartbeating, exit. The in-flight task is left alone; the reaper will reclaim it after `VISIBILITY_TIMEOUT`.

## 8. Admin UI

Server-rendered with `html/template` plus HTMX for in-page updates. Mounted on the HTTP server. Pico.css is served from `/static/` for baseline styling.

### 8.1 Routes

| Method | Path                       | Purpose                                                        |
|--------|----------------------------|----------------------------------------------------------------|
| GET    | `/`                        | Redirect to `/tasks`                                           |
| GET    | `/tasks`                   | Table of tasks with status filter + pagination                 |
| GET    | `/tasks/fragment`          | HTMX poll endpoint — returns just the table body, every 3s     |
| GET    | `/tasks/new`               | Form to create a new task                                      |
| POST   | `/tasks`                   | Create task (HTMX) — returns the new table row                 |
| GET    | `/tasks/{id}`              | Task detail (status, claim info, heartbeat, last_error)        |
| GET    | `/tasks/{id}/edit`         | Edit form (only editable while `pending`)                      |
| POST   | `/tasks/{id}`              | Update task (HTMX)                                             |
| POST   | `/tasks/{id}/delete`       | Delete (only when terminal or `pending`)                       |
| POST   | `/tasks/{id}/requeue`      | Reset a `failed`/`completed` task to `pending`                 |

### 8.2 Form fields

`name` (required), `priority` (int, default 0), `scheduled_for` (optional datetime-local), `max_attempts` (int, default 3), `payload` (JSON textarea; server validates parseable JSON).

### 8.3 Auth

HTTP basic auth middleware on everything under `/tasks*` and `/static/`. Credentials from `ADMIN_USER` / `ADMIN_PASSWORD`. `/healthz`, `/readyz`, `/metrics` remain unauthenticated (for infra probes / scrapers).

## 9. Configuration

Env vars loaded via `envconfig` (or equivalent). No config files.

| Var                  | Mastermind | Worker | Default          | Notes                              |
|----------------------|:---------:|:------:|------------------|------------------------------------|
| `DATABASE_URL`       | ✅        | —      | —                | Postgres DSN                       |
| `GRPC_LISTEN_ADDR`   | ✅        | —      | `:50051`         |                                    |
| `HTTP_LISTEN_ADDR`   | ✅        | —      | `:8080`          |                                    |
| `MASTERMIND_ADDR`    | —         | ✅     | —                | e.g. `mastermind:50051`            |
| `WORKER_TOKEN`       | ✅        | ✅     | —                | Shared bearer token                |
| `ADMIN_USER`         | ✅        | —      | —                |                                    |
| `ADMIN_PASSWORD`     | ✅        | —      | —                |                                    |
| `VISIBILITY_TIMEOUT` | ✅        | —      | `30s`            | Reaper threshold                   |
| `REAPER_INTERVAL`    | ✅        | —      | `10s`            |                                    |
| `POLL_INTERVAL`      | —         | ✅     | `2s`             |                                    |
| `HEARTBEAT_INTERVAL` | —         | ✅     | `10s`            |                                    |
| `LOG_LEVEL`          | ✅        | ✅     | `info`           | `debug` / `info` / `warn` / `error`|
| `LOG_FORMAT`         | ✅        | ✅     | `json`           | `json` / `text`                    |

All required vars without defaults must be present at startup or the process exits with a clear error.

## 10. Observability

- **Logging:** `log/slog` with JSON handler in prod, text handler in dev (`LOG_FORMAT`). Standard fields: `component` (`mastermind` / `worker`), `worker_id`, `task_id`, `attempt`, `error`.
- **Metrics:** Prometheus exposed on mastermind `/metrics`.
  - `tasks_claimed_total{result}` counter
  - `tasks_completed_total` counter
  - `tasks_failed_total{reason}` counter (`exhausted` / `report_error` / `lease_expired`)
  - `tasks_reaped_total` counter
  - `tasks_pending` gauge (sampled every reaper tick)
  - `claim_duration_seconds` histogram
- **Health:** `/healthz` returns 200 if the process is up. `/readyz` returns 200 iff `SELECT 1` against Postgres succeeds.

## 11. Testing Strategy

- **Unit tests** for DB operations (claim query, heartbeat, report, reaper) against a real Postgres via `testcontainers-go`. The claim query is too subtle to mock.
- **Concurrency test**: enqueue 100 tasks, start 10 goroutine workers hitting an in-process mastermind, assert every task is completed exactly once and counts match.
- **Reaper test**: insert a task in `running` state with stale `last_heartbeat_at`, run the reaper, assert it transitions to `pending` with incremented `attempt_count` and that `scheduled_for` is set.
- **Retry test**: report a task as failed, assert retry with backoff; repeat until `max_attempts`, assert terminal `failed`.
- **HTTP handler tests**: `httptest` against handlers with a real DB. Cover create / edit / requeue / delete.
- **gRPC handler tests**: use `bufconn` to stand up an in-memory gRPC server + real DB.

## 12. Deployment

- Two multi-stage Dockerfiles (one per binary), final stage distroless.
- Both binaries configured entirely via env vars — drop-in for Coolify or any container platform.
- Mastermind runs `migrate up` at startup before binding listeners. Migration failures abort startup.
- Worker fails fast if it cannot resolve / connect to `MASTERMIND_ADDR` on startup (after a short initial backoff window to tolerate rolling deploys).
- Graceful shutdown on SIGTERM for both binaries with a 15s drain window.

## 13. Dependencies (proposed)

- `google.golang.org/grpc` + `google.golang.org/protobuf`
- `github.com/jackc/pgx/v5` (pgxpool) for Postgres
- `github.com/golang-migrate/migrate/v4`
- `github.com/prometheus/client_golang`
- `github.com/kelseyhightower/envconfig` (or equivalent)
- `github.com/google/uuid`
- HTMX + Pico.css as static assets (not Go deps)

No external queue/broker. Postgres is the queue.

## 14. Open Questions (deferred)

- **Real task handlers**: out of scope for this iteration. The `payload` field is intentionally opaque JSON so handlers can be added later without a schema change.
- **Per-worker concurrency > 1**: deferred. Worker currently runs a single claim loop. If needed, add a `WORKER_CONCURRENCY` env var and spawn N identical claim goroutines sharing one gRPC connection.
- **mTLS / per-worker identity**: deferred. Shared bearer token is sufficient behind a trusted network perimeter.
