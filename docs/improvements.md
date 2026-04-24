# Improvement & feature ideas

Brainstormed follow-ups for `el-pulpo-ai`. Each entry is a rough direction — not a
committed plan. Intended as input for prioritisation and future design docs.

Context: `el-pulpo-ai` is a Go distributed task queue. A single **mastermind**
(gRPC + HTMX admin UI, Postgres-backed) fans tasks out to horizontally scalable
**workers**, each of which bridges one task at a time to an on-host coding
agent over MCP. A stdio `mastermind-mcp` binary gives coding agents a way to
create and inspect tasks from the other direction.

## Reliability & operations

1. **Task cancellation, end-to-end.** Admin-initiated cancel that flows through
   mastermind → worker → MCP agent. Requires a `cancel_requested` column, a
   cancel-aware tool in the worker MCP surface, and cooperative shutdown on the
   agent side. Today there is no clean way to stop a running task.

2. **Retry policy per task.** Replace the single `max_attempts` integer with a
   configurable backoff policy (exponential + jitter, min/max interval, retry
   on which error classes). Expose defaults via env, override per task on
   `CreateTask`.

3. **Dead-letter queue + retry UI.** Permanently failed tasks (attempts
   exhausted) move to a DLQ view in the admin UI with one-click "requeue with
   reset attempts". Gives operators a recovery path that does not require SQL.

4. **Graceful shutdown with drain.** On SIGTERM the worker should stop
   claiming, signal the agent via MCP, wait up to a configurable grace period
   for `complete_task`/`fail_task`, then release the claim so the reaper does
   not have to pick it up. Today shutdown relies on the heartbeat reaper.

5. **Structured audit log.** Append-only log of every admin-initiated action
   (create, cancel, requeue, config change) with actor identity, timestamp,
   and before/after state. Useful when multiple humans and agents are both
   creating tasks through different surfaces.

## Scheduling & routing

6. **Scheduled and recurring tasks.** First-class cron-like schedules with
   timezone support. Stored as a separate `schedules` table; a background loop
   in mastermind materialises due occurrences into `tasks` rows. Replaces the
   current workaround of external cron calling the gRPC API.

7. **Worker tags and task routing.** Workers advertise capability tags
   (`lang=go`, `gpu=true`, `region=eu-west-1`) on register/heartbeat; tasks
   carry a required-tags selector. The claim query filters. Lets one fleet
   host heterogeneous agents without splitting deployments.

8. **Task dependencies (DAG).** A task can declare prerequisite task IDs and
   only becomes claimable once they complete successfully. Opens the door to
   multi-step agent workflows (plan → implement → review) modeled as separate
   tasks rather than baked into one agent session.

9. **Priority fairness.** Current claim query is `ORDER BY priority DESC,
   created_at ASC` which starves low-priority tasks during priority bursts.
   Add weighted fair queuing so every priority level gets a share of throughput
   proportional to its weight.

10. **Idempotency keys on `CreateTask`.** Optional client-supplied dedup key
    with a uniqueness constraint (and a TTL). Safe retries from coding agents
    and external services without creating duplicate work.

## Observability

11. **OpenTelemetry tracing across the chain.** Instrument gRPC and the worker
    MCP server so a single trace covers `CreateTask` → claim → agent tool
    calls → completion. Emit exemplars on the Prometheus metrics so operators
    can jump from a latency spike to the offending trace.

12. **Real-time admin UI updates via SSE.** Replace HTMX polling on the task
    detail page with a Server-Sent Events endpoint that pushes status,
    progress note, and appended log lines. Cheaper under load and gives a
    live-tail feel for long-running agents.

13. **Task result artifacts.** Alongside the existing log lines, capture
    structured output (JSON blob, file references, links) on `complete_task`.
    Surface on the task detail page. Today everything useful has to be stuffed
    into free-text log lines.

14. **Prebuilt Grafana dashboards.** Ship dashboard JSON in `deploy/grafana/`
    covering queue depth by priority, claim-to-start latency, per-worker
    throughput, failure rate by task name. Make operability a zero-config
    experience.

## Security & multi-tenancy

15. **Roles and OIDC for the admin UI.** Replace the single
    `ADMIN_USER`/`ADMIN_PASSWORD` pair with OIDC + role mapping
    (viewer / operator / admin). Viewer cannot retry or cancel; operator can
    but cannot change config; admin can do everything. Removes the "shared
    password in env" footgun.

16. **Per-worker token rotation.** Issue short-lived worker tokens (JWT or
    opaque) on registration, rotated periodically, instead of a single
    long-lived shared `WORKER_TOKEN`. Limits blast radius when one worker host
    is compromised.

17. **Namespaces / projects.** Tag every task with a namespace, enforce quotas
    and access control per namespace, scope admin views by namespace. Lets one
    mastermind serve multiple teams without leaking visibility.

18. **Task payload schema registry.** Register a JSON Schema per task `name`;
    reject `CreateTask` calls whose payload doesn't validate. Stops shape
    drift between producers and the agents that consume the payload.

## Developer experience

19. **`elpulpo` CLI.** A single Go binary that wraps the admin gRPC surface
    (`elpulpo tasks create|get|list|cancel|retry`, `elpulpo workers list`).
    Uses the same auth as `mastermind-mcp`. Faster than `curl`ing the HTTP
    admin API for ad-hoc ops.

20. **Helm chart / Kustomize base.** Production-grade Kubernetes manifests
    under `deploy/helm/` with sensible defaults for the mastermind Deployment,
    the worker DaemonSet/Deployment, Postgres (external by default), TLS
    termination, Prometheus ServiceMonitor, and a HorizontalPodAutoscaler
    driven by the queue-depth metric. Today every user reinvents this.
