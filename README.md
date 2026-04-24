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
make test-e2e       # end-to-end tests against the full docker-compose stack
```

The end-to-end suite is documented in [`e2e/README.md`](e2e/README.md) and
designed in
[`docs/superpowers/specs/2026-04-24-e2e-testing-design.md`](docs/superpowers/specs/2026-04-24-e2e-testing-design.md).

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

Tools: `create_task`, `get_task`, `list_tasks`. See the design doc for the full input/output schemas.

Keep `ADMIN_TOKEN` out of version control — source it from your shell environment or a secrets manager. Set `MASTERMIND_TLS=true` when mastermind is not on localhost; otherwise the bearer token is sent in cleartext.

## Metrics / Health

- `GET /metrics`  — Prometheus scrape target
- `GET /healthz`  — liveness
- `GET /readyz`   — pings the database

## Architecture

See the design doc linked above.
