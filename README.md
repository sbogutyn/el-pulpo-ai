# el-pulpo-ai

Distributed task queue. Single **mastermind** (gRPC + HTMX admin UI, Postgres-backed) and horizontally-scalable **workers** that bridge the queue to an on-host coding agent over MCP.

Design: [`docs/superpowers/specs/2026-04-23-mastermind-worker-task-queue-design.md`](docs/superpowers/specs/2026-04-23-mastermind-worker-task-queue-design.md)

## Local development

```bash
make dev-up         # start Postgres via docker-compose
make migrate-up     # apply migrations
make run-mastermind # run mastermind locally
make run-worker     # run a worker locally
```

## Tests

```bash
make test           # unit + integration tests (uses testcontainers)
```

## Configuration

Both binaries are configured via environment variables only. See
[`docs/superpowers/specs/2026-04-23-mastermind-worker-task-queue-design.md`](docs/superpowers/specs/2026-04-23-mastermind-worker-task-queue-design.md#9-configuration)
for the complete table.

Minimum to run the mastermind:

```bash
DATABASE_URL=postgres://pulpo:pulpo@localhost:5432/pulpo?sslmode=disable \
WORKER_TOKEN=devtoken \
ADMIN_USER=admin ADMIN_PASSWORD=admin \
go run ./cmd/mastermind
```

Minimum to run a worker:

```bash
MASTERMIND_ADDR=localhost:50051 \
WORKER_TOKEN=devtoken \
go run ./cmd/worker
```

The worker does not execute task logic directly. It connects to mastermind over
gRPC and serves a local MCP endpoint (default `http://127.0.0.1:7777/mcp`) that
a coding agent on the same machine uses to drive one task at a time.

## Worker MCP (coding agent integration)

Point a local coding agent at the worker's MCP endpoint and it will see these
tools:

| Tool | Purpose |
| ---- | ------- |
| `claim_next_task` | Claim the next task from mastermind (idempotent while already holding one). |
| `get_current_task` | Return details of the task the worker is currently holding. |
| `update_progress` | Set the short "current status" note surfaced on the admin UI. |
| `append_log` | Append one immutable line to the task's log. |
| `set_jira_url` | Attach a JIRA URL to the claimed task (allowed any time during the claim). |
| `open_pr` | Atomically transition to `pr_opened`, set `github_pr_url`, and release the claim. After this call the worker is idle; finalization is admin-only. |
| `complete_task` | Mark the task successful and release the claim. |
| `fail_task` | Mark the task failed with a message; mastermind retries or terminates per policy. |

Example `.mcp.json` for a worker running on `127.0.0.1:7777`:

```json
{
  "mcpServers": {
    "el-pulpo-worker": {
      "type": "http",
      "url": "http://127.0.0.1:7777/mcp"
    }
  }
}
```

The worker binds to loopback by default and benefits from the MCP SDK's
DNS-rebinding protection — override with `WORKER_MCP_LISTEN_ADDR` if needed,
but do not bind publicly without adding transport-level auth.

Once a worker calls `open_pr`, the task is "parked" — it leaves `in_progress`, the
claim is released, and only an admin (via `mastermind-mcp`, `elpulpo`, or the admin
UI) can finalize it with `complete`/`fail` or move it to `review_requested`. The
worker is freed to claim another task immediately.

## Admin UI

Open http://localhost:8080 (basic-auth with `ADMIN_USER` / `ADMIN_PASSWORD`).

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

Tools: `create_task`, `get_task`, `list_tasks`, `request_review`, `finalize_task`. See the design doc for the full input/output schemas.

Keep `ADMIN_TOKEN` out of version control — source it from your shell environment or a secrets manager. Set `MASTERMIND_TLS=true` when mastermind is not on localhost; otherwise the bearer token is sent in cleartext.

## `elpulpo` CLI

`elpulpo` is a single Go binary that wraps the mastermind admin gRPC surface
so operators can do ad-hoc queue operations from a shell without curling the
HTMX admin UI. It uses the same bearer-token environment as `mastermind-mcp`
(`MASTERMIND_ADDR`, `ADMIN_TOKEN`, `MASTERMIND_TLS`), so one `.env` serves
both.

Build and run:

```bash
make build-cli                   # -> bin/elpulpo
bin/elpulpo help                 # full usage reference

# convenience: pass positional args through Make
make run-cli ARGS="tasks list"
```

Commands:

| Command | Purpose |
| ------- | ------- |
| `elpulpo tasks create --name NAME [--instructions TEXT\|@file\|-] [flags]` | Enqueue a new task. Tasks created via gRPC require `payload.instructions`; `--instructions` is a convenience that wraps text into the canonical payload (mergeable with `--payload`). `--payload` accepts inline JSON, `@path` for a file, or `-` for stdin. |
| `elpulpo tasks get <id>` | Show one task's full state. |
| `elpulpo tasks list [flags]` | Table of tasks; filter with `--status`, paginate with `--limit`/`--offset`, emit JSON with `--json`. |
| `elpulpo tasks cancel <id>` | Remove a task. Rejects tasks that are currently claimed or running. |
| `elpulpo tasks retry <id>` | Reset a pending/completed/failed task back to `pending` with a fresh attempt count. |
| `elpulpo tasks request-review <id>` | Move a `pr_opened` task to `review_requested`. |
| `elpulpo tasks finalize <id> --success` | Terminal admin completion of a parked task. |
| `elpulpo tasks finalize <id> --fail "reason"` | Terminal admin failure of a parked task. |
| `elpulpo workers list` | Distinct worker identities seen in the tasks table with active / completed / failed counts and last-seen timestamp. |

Tasks created via the gRPC `AdminService.CreateTask` (used by `mastermind-mcp`,
`elpulpo`, and the admin UI) must include a non-empty `payload.instructions`
string — the canonical "what should the agent do" surface. The `elpulpo tasks
create --instructions` flag is a convenience for setting it without hand-rolling
the JSON.

Set `MASTERMIND_TLS=true` when mastermind is not on localhost; otherwise the
bearer token is sent in cleartext. `REQUEST_TIMEOUT` (default `15s`) caps any
single RPC.

## Metrics / Health

- `GET /metrics`  — Prometheus scrape target
- `GET /healthz`  — liveness
- `GET /readyz`   — pings the database

## Architecture

See the design doc linked above.
