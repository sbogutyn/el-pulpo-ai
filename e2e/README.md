# End-to-end test harness

Production-like test suite for el-pulpo. Brings up the full stack
(`postgres` + `mastermind` + `worker`) via `docker compose`, then drives
every externally-observable feature — gRPC, the HTMX admin UI, the worker
MCP endpoint, the `mastermind-mcp` stdio binary, the reaper, and the
Prometheus metrics — using the same clients a production caller would.

See the design doc for the full rationale and coverage matrix:
[`docs/superpowers/specs/2026-04-24-e2e-testing-design.md`](../docs/superpowers/specs/2026-04-24-e2e-testing-design.md).

## Running

From the repository root:

```bash
make test-e2e
```

Under the hood this runs:

```bash
go test -tags=e2e -timeout=15m ./e2e/...
```

The first run builds the three Docker images (`el-pulpo-mastermind`,
`el-pulpo-worker`, `el-pulpo-mastermind-mcp`) and pulls `postgres:16-alpine`
and `busybox:1.36` for the healthcheck sidecars. Subsequent runs reuse the
build cache.

## Interactive debugging

Want to poke the stack while investigating a failure? Set `E2E_KEEP=1` and
the `TestMain` teardown is skipped:

```bash
E2E_KEEP=1 make test-e2e
# ... tests ran ...
# stack is still up; reach it at:
#   Postgres   localhost:15432  (pulpo / pulpo)
#   gRPC       localhost:15051
#   admin HTTP http://localhost:18080  (e2e / e2e)
#   worker MCP http://localhost:17777/mcp
docker compose -f docker-compose.e2e.yml down -v  # when done
```

Re-run the tests against the already-running stack with `E2E_SKIP_STACK=1`
to skip the `compose up` phase.

## Ports and credentials

All listeners are bound to `127.0.0.1` at deliberately non-default ports so
the suite does not collide with a developer's `make dev-up`
/ `make run-mastermind` / `make run-worker` session.

| Service | Host port | Purpose |
|---------|-----------|---------|
| Postgres | 15432 | raw SQL probing if needed |
| Mastermind gRPC | 15051 | `TaskService`, `AdminService` |
| Mastermind HTTP | 18080 | admin UI, `/healthz`, `/readyz`, `/metrics` |
| Worker MCP | 17777 | `POST /mcp` (streamable HTTP) + `/healthz` |

Test-only credentials (see `docker-compose.e2e.yml`):

| Secret | Value |
|--------|-------|
| `WORKER_TOKEN` | `e2e-worker-token` |
| `ADMIN_TOKEN` | `e2e-admin-token` |
| `ADMIN_USER` / `ADMIN_PASSWORD` | `e2e` / `e2e` |

These are not secrets in any meaningful sense — they only guard the
ephemeral e2e stack.

## Files

| File | Purpose |
|------|---------|
| `stack_test.go` | Docker-compose wrapper (`Up`, `Down`, log dump). |
| `clients_test.go` | gRPC, HTTP, and MCP client helpers. |
| `testmain_test.go` | `TestMain` — compose up / build mastermind-mcp / compose down. |
| `admin_grpc_test.go` | `AdminService` coverage. |
| `worker_grpc_test.go` | `TaskService` coverage + auth matrix. |
| `admin_http_test.go` | Admin UI + HTMX fragment + static auth. |
| `probes_test.go` | `/healthz`, `/readyz`, `/metrics`, worker `/healthz`. |
| `worker_mcp_test.go` | Worker MCP tools: claim/get/progress/log/complete/fail. |
| `mastermind_mcp_test.go` | `mastermind-mcp` stdio tools + auth failure. |
| `reaper_test.go` | Lease expiration → requeue / terminal. |
| `journey_test.go` | End-to-end integration across all three binaries. |

## Requirements

- Docker (Engine 24+ or Docker Desktop) with Compose v2.
- Go 1.25+ (matches `go.mod`).
- ~1.5GB of disk for the Docker images and Go build cache.

The suite is excluded from the default `go test ./...` run by the
`//go:build e2e` tag — nothing changes for developers who do not opt in.
