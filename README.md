# el-pulpo-ai

Distributed task queue. Single **mastermind** (gRPC + HTMX admin UI, Postgres-backed) and horizontally-scalable **workers**.

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

## Admin UI

Open http://localhost:8080 (basic-auth with `ADMIN_USER` / `ADMIN_PASSWORD`).

## Metrics / Health

- `GET /metrics`  — Prometheus scrape target
- `GET /healthz`  — liveness
- `GET /readyz`   — pings the database

## Architecture

See the design doc linked above.
