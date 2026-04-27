# Worker Real Tasks — Design

**Date:** 2026-04-27
**Status:** Approved for planning
**Language / Runtime:** Go

## 1. Goal

Move the mastermind/worker queue from a "fake handler sleeps for one minute" scaffold to a model where workers carry **real tasks** — text instructions a coding agent acts on — through a richer lifecycle that includes opening a pull request, requesting review, and being finalized by an admin.

Three coupled changes:

1. **Task content.** Tasks are text instructions surfaced to the agent under a canonical key.
2. **State machine.** Worker advances `claimed → in_progress → pr_opened`; once a PR is open the claim is released ("parked") and only an admin can finalize. Server validates every transition.
3. **Worker-set metadata.** The agent attaches `jira_url` (any time during a claim) and `github_pr_url` (atomically with `open_pr`) to its claimed task.

## 2. Non-goals

- Webhook- or poller-driven finalization. Mastermind does not call GitHub; admins finalize.
- Re-claiming parked tasks. Once a task is `pr_opened`/`review_requested`, no worker re-engages with it.
- JSON Schema validation of `payload` beyond the `instructions` field (deferred to improvement #18).
- Per-task retry policy beyond `max_attempts` (improvement #2).
- Cancellation of running worker work (improvement #1).
- Multi-tenant scoping, RBAC, or namespacing (improvements #15–17).

## 3. Decisions (recap)

| Topic | Decision |
| --- | --- |
| Task content | Keep `payload JSONB`. Server enforces non-empty `payload.instructions` string at `CreateTask`. |
| Pipeline shape | Linear: `claimed → in_progress → pr_opened → review_requested → completed | failed`. |
| Worker attachment | Park-and-resume: `open_pr` releases the claim. Worker is freed; admin finalizes. |
| Parked-task transitions | Admin only (CLI / mastermind-mcp / admin UI). No webhook, no polling, no worker re-claim. |
| Metadata | `jira_url` via dedicated worker call, any time during claim. `github_pr_url` only via `open_pr` (atomic with state change). |
| Retry | Worker `fail` from `in_progress` retries while `attempt_count < max_attempts`. Admin `finalize(failure)` from parked states is always terminal. |

## 4. State machine

```
pending ──ClaimTask──► claimed ──first Heartbeat──► in_progress
                                                        │
                              ┌─worker fail (attempts left)─┤
                              ▼                             │
                           pending                          │
                              │                             │
                              └─exhausted──► failed         │
                                                            │
                              ┌─worker complete─────────────┤  (no-PR tasks)
                              ▼                             │
                           completed                        │
                                                            │
                              ┌─worker OpenPR(url)──────────┘  atomically:
                              │                                  status → pr_opened
                              │                                  github_pr_url set
                              │                                  claim released
                              ▼
                          pr_opened ──admin RequestReview──► review_requested
                              │                                       │
                              └────────admin FinalizeTask──────────────┘
                                       (success → completed)
                                       (failure → failed, terminal)
```

### 4.1 Transition matrix

| From → To | Caller | Side effects |
|---|---|---|
| `pending` → `claimed` | worker (`ClaimTask`) | sets `claimed_by`, `claimed_at`, `last_heartbeat_at` |
| `claimed` → `in_progress` | worker (`Heartbeat`) | first heartbeat after claim |
| `in_progress` → `pending` | worker (`ReportResult` fail, attempts left) | clears claim, `attempt_count++` |
| `in_progress` → `failed` | worker (`ReportResult` fail, exhausted) | terminal |
| `in_progress` → `completed` | worker (`ReportResult` success) | terminal, no PR path |
| `in_progress` → `pr_opened` | worker (`OpenPR`) | sets `github_pr_url`, clears claim, stops heartbeat |
| `claimed` / `in_progress` → no-op | worker (`UpdateProgress` / `AppendLog` / `SetJiraURL`) | no state change; refreshes lease |
| `pr_opened` → `review_requested` | admin (`RequestReview`) | informational |
| `pr_opened` / `review_requested` → `completed` | admin (`FinalizeTask` success) | terminal |
| `pr_opened` / `review_requested` → `failed` | admin (`FinalizeTask` failure) | terminal, no `attempt_count` change |
| `pending` / `completed` / `failed` → `pending` | admin (`RetryTask`, existing) | resets, `attempt_count = 0`, **clears `github_pr_url`**, preserves `jira_url` |
| `pending` / `completed` / `failed` → deleted | admin (`CancelTask`, existing) | unchanged |

`RetryTask` from `pr_opened` / `review_requested` returns `FAILED_PRECONDITION`. Operators must `FinalizeTask` first, then `RetryTask`.

## 5. Schema changes

### 5.1 Migration `000005_extend_task_states`

```sql
-- 000005_extend_task_states.up.sql
ALTER TYPE task_status RENAME VALUE 'running' TO 'in_progress';
ALTER TYPE task_status ADD VALUE 'pr_opened'        AFTER 'in_progress';
ALTER TYPE task_status ADD VALUE 'review_requested' AFTER 'pr_opened';

-- Recreate the heartbeat index against the new value name. CONCURRENTLY
-- avoids the table lock on the existing system.
DROP INDEX IF EXISTS idx_tasks_heartbeat;
CREATE INDEX CONCURRENTLY idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'in_progress');
```

`idx_tasks_claimable` (filtering `status = 'pending'`) is unchanged. `pr_opened` and `review_requested` are intentionally absent from `idx_tasks_heartbeat`: parked tasks have no claim, no lease, no reaper involvement.

The down migration renames `in_progress` back to `running` and recreates the heartbeat index against `('claimed', 'running')`. It does **not** remove the `pr_opened` / `review_requested` enum values — Postgres does not support dropping enum values without recreating the type. Operators rolling back must first ensure no rows reference those values (cancel or finalize all parked tasks); the down migration leaves the unused values in place as inert enum members. This is acceptable because rollback is not part of the normal operational flow for this change.

### 5.2 No payload schema change

Validation is Go-side only at `CreateTask`. Existing rows (which may have arbitrary `payload`) are not touched. Workers reading legacy rows see whatever `payload` was stored.

## 6. Wire surface

### 6.1 `TaskService` (worker, `WORKER_TOKEN`)

Two new RPCs; existing RPCs unchanged.

```proto
rpc SetJiraURL(SetJiraURLRequest) returns (SetJiraURLResponse);
rpc OpenPR(OpenPRRequest)         returns (OpenPRResponse);

message SetJiraURLRequest  { string worker_id = 1; string task_id = 2; string url = 3; }
message SetJiraURLResponse {}

message OpenPRRequest  { string worker_id = 1; string task_id = 2; string github_pr_url = 3; }
message OpenPRResponse {}
```

- `SetJiraURL` is allowed from `claimed` or `in_progress`. It refreshes the lease (same effect as `Heartbeat`).
- `OpenPR` requires the caller to currently hold the claim and the source state to be `in_progress`. It atomically: writes `github_pr_url`, sets `status = 'pr_opened'`, clears `claimed_by` / `claimed_at` / `last_heartbeat_at`. Empty `github_pr_url` returns `INVALID_ARGUMENT`.
- `Heartbeat`, `UpdateProgress`, `AppendLog`, `ReportResult`, `ClaimTask` keep their existing semantics. `ReportResult` continues to be the worker's terminal call from `in_progress` (success or fail-with-retry).

### 6.2 `AdminService` (`ADMIN_TOKEN`)

Two new RPCs.

```proto
rpc RequestReview(RequestReviewRequest) returns (RequestReviewResponse);
rpc FinalizeTask(FinalizeTaskRequest)   returns (FinalizeTaskResponse);

message RequestReviewRequest  { string id = 1; }
message RequestReviewResponse { TaskDetail task = 1; }

message FinalizeTaskRequest {
  string id = 1;
  oneof outcome {
    Success success = 2;
    Failure failure = 3;
  }
  message Success {}
  message Failure { string message = 1; }
}
message FinalizeTaskResponse { TaskDetail task = 1; }
```

`CreateTask`, `GetTask`, `ListTasks`, `ListTaskLogs`, `CancelTask`, `RetryTask`, `ListWorkers` unchanged in shape. `CreateTask` gains the `payload.instructions` validator at the handler boundary (returns `INVALID_ARGUMENT` on failure).

### 6.3 Worker MCP surface

`internal/worker/mcpserver/tools.go`:

| Tool | Status | Notes |
| ---- | ------ | ----- |
| `claim_next_task` | unchanged | `TaskView` now includes `instructions`, `jira_url`, `github_pr_url`. |
| `get_current_task` | unchanged (view tweak) | same view fields as above. |
| `update_progress` | unchanged | |
| `append_log` | unchanged | |
| `set_jira_url` | **new** | one string arg; calls `TaskService.SetJiraURL`. |
| `open_pr` | **new** | requires `github_pr_url`; on success clears `mcpserver.State.current` (reuses the `complete`/`fail` clear path) so the agent is now idle and may `claim_next_task` again. |
| `complete_task` | unchanged | still the terminal path for tasks that don't need a PR. |
| `fail_task` | unchanged | retry semantics enforced server-side. |

The new tool descriptions explicitly state: "after `open_pr`, the worker is idle. Finalization (`complete` / `fail`) is performed by an admin via the `mastermind-mcp` server, the `elpulpo` CLI, or the admin UI."

### 6.4 Admin callers

- **`elpulpo` CLI** — `elpulpo tasks request-review <id>`, `elpulpo tasks finalize <id> --success`, `elpulpo tasks finalize <id> --fail "msg"`. `tasks create` gains `--instructions @file.md` / `--instructions -` to populate `payload.instructions` from a file or stdin without forcing the caller to hand-build JSON.
- **`mastermind-mcp`** — `request_review`, `finalize_task` MCP tools mirroring the gRPC.
- **HTMX admin UI** — task detail page renders `instructions` (rendered as plaintext, not HTML), shows `jira_url` and `github_pr_url` as links when present, and on parked tasks adds two action buttons: *Mark reviewed* (`RequestReview`) and *Finalize* (form with success/failure radio plus optional message). Both call back into the existing handler structure (`internal/mastermind/httpserver`).

## 7. Server internals

### 7.1 Transition validation (one place)

A new `internal/mastermind/store/transitions.go` holds the canonical allow-list:

```go
type role int
const (
    roleWorker role = iota + 1
    roleAdmin
)

type transitionKey struct {
    role   role
    action string
}

var allowedFrom = map[transitionKey][]string{
    {roleWorker, "open_pr"}:        {"in_progress"},
    {roleWorker, "set_jira_url"}:   {"claimed", "in_progress"},
    {roleWorker, "complete"}:       {"in_progress"},
    {roleWorker, "fail"}:           {"in_progress"},
    {roleAdmin,  "request_review"}: {"pr_opened"},
    {roleAdmin,  "finalize"}:       {"pr_opened", "review_requested"},
    {roleAdmin,  "retry"}:          {"pending", "completed", "failed"},
}
```

Each store function (`OpenPR`, `SetJiraURL`, `RequestReview`, `FinalizeTask`) issues one conditional `UPDATE ... WHERE id = $1 AND status = ANY($2)` (plus `claimed_by = $3` for worker actions). Zero rows affected → `FAILED_PRECONDITION`. Existing `lifecycle.go` already uses this pattern; new code follows it. The map is the source of truth for tests.

### 7.2 `OpenPR` is one statement

```sql
UPDATE tasks
   SET status            = 'pr_opened',
       github_pr_url     = $3,
       claimed_by        = NULL,
       claimed_at        = NULL,
       last_heartbeat_at = NULL,
       updated_at        = now()
 WHERE id = $1
   AND status = 'in_progress'
   AND claimed_by = $2
RETURNING id;
```

Atomic with the state change. The worker's local `mcpserver.State` clears on success so a stale heartbeat goroutine cannot keep firing after the claim is gone.

### 7.3 Payload validator

```go
func validateInstructions(payload []byte) error {
    var v struct{ Instructions string `json:"instructions"` }
    if err := json.Unmarshal(payload, &v); err != nil {
        return fmt.Errorf("payload is not valid JSON: %w", err)
    }
    if strings.TrimSpace(v.Instructions) == "" {
        return errors.New(`payload.instructions must be a non-empty string`)
    }
    return nil
}
```

Called from `internal/mastermind/grpcserver` `CreateTask` before the store insert. Surfaced as gRPC `INVALID_ARGUMENT`.

### 7.4 Reaper

`internal/mastermind/reaper` already targets `status IN ('claimed', 'running')`. Updated to `('claimed', 'in_progress')`. `pr_opened` and `review_requested` are explicitly excluded — those rows have no `claimed_by`, no `last_heartbeat_at`, and live until an admin acts on them.

`reaper.Reclaim` continues to either reset to `pending` (attempts left) or transition to terminal `failed` (exhausted). Same code path a worker `fail` triggers.

### 7.5 Retry behavior

`RetryTask` (`internal/mastermind/store/lifecycle.go`):

- Allowed from `pending`, `completed`, `failed`. Returns `FAILED_PRECONDITION` from `claimed`, `in_progress`, `pr_opened`, `review_requested`.
- On reset: `status = 'pending'`, `attempt_count = 0`, `last_error = NULL`, `claimed_by/_at = NULL`, `last_heartbeat_at = NULL`, **`github_pr_url = NULL`**, `jira_url` preserved.
- The `github_pr_url` clear is the only behavior change to `RetryTask`. A fresh attempt opens a new PR; carrying the old URL would mislead operators reading the admin UI.

## 8. Migration & rollout

### 8.1 Order of operations

1. **DB migration** `000005_extend_task_states` — online, no downtime, no row updates required. The `running` → `in_progress` rename preserves data.
2. **Mastermind deploy.** New transition logic, new `idx_tasks_heartbeat` filter, new RPCs. Existing workers remain compatible: their `ReportResult`-driven path is still a valid sub-path of the new state machine.
3. **Worker deploy** with new MCP tools (`set_jira_url`, `open_pr`).

No feature flag. The old worker path is a strict subset of the new model.

### 8.2 `payload.instructions`

- New `CreateTask` calls require `payload.instructions`. Older callers (any direct gRPC consumers, the `mastermind-mcp` server, the `elpulpo` CLI) must be updated to populate it.
- Existing rows are untouched — workers reading legacy `payload` see an empty `instructions` and the raw JSON in the existing `payload` view field. Operators may finish or cancel those tasks normally.
- `mastermind-mcp` and `elpulpo` CLI documentation is updated; `elpulpo tasks create --instructions` is the convenience path.

### 8.3 Admin UI

The task detail template gains a rendered `instructions` block at the top, link-rendered `jira_url` / `github_pr_url` in the metadata section, and on parked-state rows the two action forms (`request_review`, `finalize`). All gated behind the existing `ADMIN_USER`/`ADMIN_PASSWORD` basic auth — no new auth surface.

## 9. Testing

Following the existing `internal/mastermind/store/*_test.go` pattern (testcontainers Postgres):

- **`transitions_test.go`** — table-driven, one row per `(role, action, from_state)` cell of the matrix. Allowed → 1 row affected; disallowed → `FAILED_PRECONDITION` and DB row unchanged. Acts as the executable spec for the state machine.
- **`open_pr_test.go`** — atomic update assertions: `status = 'pr_opened'`, `github_pr_url` set, claim fields cleared, `last_heartbeat_at` cleared. Wrong worker → `FAILED_PRECONDITION`; empty URL → `INVALID_ARGUMENT`.
- **`set_jira_url_test.go`** — allowed from `claimed`/`in_progress`, rejected when caller doesn't own claim, lease refreshed.
- **`finalize_test.go`** — admin success and failure from `pr_opened` and `review_requested`. Rejected from `in_progress` / `pending` / `completed` / `failed`.
- **`retry_test.go`** — extends existing tests: `github_pr_url` cleared on retry; rejected from parked states.
- **`reaper_test.go`** — extends to assert `pr_opened` and `review_requested` are not reaped regardless of `last_heartbeat_at`.
- **`tasks_test.go`** — `Create` rejects payloads missing or empty `instructions`.
- **Worker MCP** (`internal/worker/mcpserver/tools_test.go`) — `set_jira_url` and `open_pr` happy paths, plus assertion that `open_pr` clears `State.current` (mirrors `complete`/`fail`).
- **e2e** (`internal/e2e/e2e_test.go`) — end-to-end happy path: `CreateTask({instructions, ...})` → worker claims → `set_jira_url` → `open_pr(url)` → admin `RequestReview` → admin `FinalizeTask(success)` → terminal state is `completed` with both URLs intact.

## 10. Open questions

None. All decisions captured above are approved.
