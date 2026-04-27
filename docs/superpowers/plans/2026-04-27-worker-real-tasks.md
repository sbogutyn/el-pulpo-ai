# Worker Real Tasks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the mastermind/worker queue from the fake-handler scaffold to a real, agent-driven pipeline: tasks carry text instructions, workers advance `claimed → in_progress → pr_opened` and release the claim at `open_pr`, then admins finalize parked tasks via `request_review` / `finalize_task`. Workers attach `jira_url` (any time) and `github_pr_url` (atomically with `open_pr`).

**Architecture:** One Postgres migration extends the `task_status` enum (`running` → `in_progress`; add `pr_opened`, `review_requested`). A canonical transition allow-list lives in the store and gates four new store methods (`SetJiraURL`, `OpenPR`, `RequestReview`, `FinalizeTask`). Two new RPCs land on each of `TaskService` (worker) and `AdminService` (admin). Worker MCP gains `set_jira_url` and `open_pr` tools; mastermind-mcp and the `elpulpo` CLI gain matching admin tools. Existing tests are updated alongside renames; TDD is used for new behavior.

**Tech Stack:** Go 1.22+, PostgreSQL 16, gRPC + protobuf, `pgx/v5`, `golang-migrate`, `testcontainers-go`, `modelcontextprotocol/go-sdk`, HTMX templates.

**Spec:** [`docs/superpowers/specs/2026-04-27-worker-real-tasks-design.md`](../specs/2026-04-27-worker-real-tasks-design.md).

---

## File structure

**Created:**
- `migrations/000005_extend_task_states.up.sql` — rename `running` → `in_progress`, add `pr_opened` and `review_requested`, recreate heartbeat index.
- `migrations/000005_extend_task_states.down.sql` — rename back; leave new enum values inert (Postgres can't drop enum values without recreating the type).
- `internal/mastermind/store/transitions.go` — canonical role/action → allowed-from map and the `roleWorker`/`roleAdmin` enum.
- `internal/mastermind/store/open_pr.go` — `(*Store).OpenPR` (worker, `in_progress` → `pr_opened`, atomic with URL set + claim release).
- `internal/mastermind/store/jira.go` — `(*Store).SetJiraURL` (worker, `claimed`/`in_progress`, refreshes lease).
- `internal/mastermind/store/finalize.go` — `(*Store).RequestReview` and `(*Store).FinalizeTask` (admin).
- `internal/mastermind/store/transitions_test.go` — table-driven matrix coverage.
- `internal/mastermind/store/open_pr_test.go`, `jira_test.go`, `finalize_test.go` — focused unit tests.

**Modified:**
- `internal/proto/tasks.proto` — `SetJiraURL`, `OpenPR` on `TaskService`; `RequestReview`, `FinalizeTask` on `AdminService`. Then `make proto` regenerates `tasks.pb.go` and `tasks_grpc.pb.go`.
- `internal/mastermind/store/tasks.go` — add `StatusInProgress`, `StatusPROpened`, `StatusReviewRequested` constants; remove `StatusRunning` value (reuse Go name `StatusRunning` is dropped). Update `RequeueTask` to clear `github_pr_url` and reject from parked states.
- `internal/mastermind/store/lifecycle.go` — replace `'running'` SQL literals with `'in_progress'`; replace `('claimed','running')` with `('claimed','in_progress')`.
- `internal/mastermind/store/logs.go`, `dashboard.go`, `agent_detail.go`, `workers.go` — same SQL string replacement.
- `internal/mastermind/grpcserver/admin.go` — `payload.instructions` validator at `CreateTask`; new `RequestReview` and `FinalizeTask` handlers; updated `knownStatuses` map; updated error wording (`"running"` → `"in_progress"`).
- `internal/mastermind/grpcserver/server.go` — new `SetJiraURL`, `OpenPR` handlers.
- `internal/mastermind/reaper/reaper.go` — unchanged code; the underlying `ReapStale` SQL (in `lifecycle.go`) is what changes. Add a regression test for parked-state exclusion.
- `internal/worker/taskclient/taskclient.go` — `(*Task).SetJiraURL`, `(*Task).OpenPR`.
- `internal/worker/mcpserver/state.go` — `(*State).SetJiraURL`, `(*State).OpenPR` (clears `current` and stops heartbeat).
- `internal/worker/mcpserver/tools.go` — `set_jira_url`, `open_pr` tools; expand `TaskView` to include `instructions`, `jira_url`, `github_pr_url`.
- `internal/mcpserver/tools.go` — `request_review`, `finalize_task` tools.
- `internal/cli/cli.go`, `internal/cli/tasks.go` — `tasks request-review`, `tasks finalize` subcommands; `--instructions` flag on `tasks create`; status filter list updated.
- `internal/mastermind/httpserver/handlers.go` — status filter list updated; new `POST /tasks/{id}/request-review` and `POST /tasks/{id}/finalize` routes.
- `internal/mastermind/httpserver/templates/tasks_detail.html` — render instructions; render parked-state action buttons.
- `internal/mastermind/httpserver/templates/dashboard_fragment.html` — `running` → `in_progress` in status pill comparison.
- `internal/e2e/e2e_test.go` — happy-path PR pipeline scenario.

---

## Stage 0 — Branch and worktree

- [ ] **0.1: Confirm working tree is clean and on `master` at the spec commit**

Run:

```bash
git status
git log --oneline -3
```

Expected: working tree clean, HEAD includes commit `2d52277` (`docs: design for worker real-tasks pipeline ...`).

- [ ] **0.2: Create a feature branch**

```bash
git switch -c feat/worker-real-tasks
```

---

## Stage 1 — Migration and status rename

The rename is the first move because every later task assumes `in_progress` exists. We rename SQL and Go constants together in the same commit so the build stays green.

### Task 1.1: Add the migration

**Files:**
- Create: `migrations/000005_extend_task_states.up.sql`
- Create: `migrations/000005_extend_task_states.down.sql`

- [ ] **Step 1: Write the up migration**

Create `migrations/000005_extend_task_states.up.sql`:

```sql
ALTER TYPE task_status RENAME VALUE 'running' TO 'in_progress';
ALTER TYPE task_status ADD VALUE 'pr_opened'        AFTER 'in_progress';
ALTER TYPE task_status ADD VALUE 'review_requested' AFTER 'pr_opened';

DROP INDEX IF EXISTS idx_tasks_heartbeat;
CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'in_progress');
```

(`CREATE INDEX CONCURRENTLY` is the production recipe described in the spec, but `golang-migrate` runs each file in a transaction by default, which is incompatible with `CONCURRENTLY`. Operators applying this manually in production may run the `CONCURRENTLY` form out-of-band beforehand. The plain `CREATE INDEX` here is correct for the migrated test container and for first-time installs.)

- [ ] **Step 2: Write the down migration**

Create `migrations/000005_extend_task_states.down.sql`:

```sql
-- Postgres has no DROP VALUE for enum types; the new values become inert.
-- Operators rolling back must finalize or cancel any tasks in pr_opened or
-- review_requested first, otherwise the rename below errors out.
ALTER TYPE task_status RENAME VALUE 'in_progress' TO 'running';

DROP INDEX IF EXISTS idx_tasks_heartbeat;
CREATE INDEX idx_tasks_heartbeat
    ON tasks (last_heartbeat_at)
    WHERE status IN ('claimed', 'running');
```

- [ ] **Step 3: Verify the migration applies cleanly in the store test setup**

Run:

```bash
go test ./internal/mastermind/store -run TestMain -count=1
```

Expected: PASS (the testcontainers Postgres receives all 5 migrations and existing tests do not yet run their bodies because no test matches `TestMain` directly — but the `TestMain` setup will fail loudly if a migration is malformed). If the run reports `0 tests`, that's the expected signal that migrations applied.

- [ ] **Step 4: Commit**

```bash
git add migrations/000005_extend_task_states.up.sql migrations/000005_extend_task_states.down.sql
git commit -m "feat(migrations): rename task_status running to in_progress and add pr_opened/review_requested"
```

### Task 1.2: Rename `running` to `in_progress` across Go code

**Files:**
- Modify: `internal/mastermind/store/tasks.go`
- Modify: `internal/mastermind/store/lifecycle.go`
- Modify: `internal/mastermind/store/logs.go`
- Modify: `internal/mastermind/store/dashboard.go`
- Modify: `internal/mastermind/store/agent_detail.go`
- Modify: `internal/mastermind/store/workers.go`
- Modify: `internal/mastermind/store/lifecycle_test.go`
- Modify: `internal/mastermind/store/logs_test.go`
- Modify: `internal/mastermind/store/tasks_update_test.go`
- Modify: `internal/mastermind/grpcserver/admin.go`
- Modify: `internal/mastermind/httpserver/handlers.go`
- Modify: `internal/mastermind/httpserver/handlers_test.go`
- Modify: `internal/mastermind/httpserver/templates/dashboard_fragment.html`
- Modify: `internal/mastermind/httpserver/templates/tasks_detail.html`

- [ ] **Step 1: Update the `TaskStatus` constants in `internal/mastermind/store/tasks.go`**

Replace the existing block:

```go
const (
    StatusPending   TaskStatus = "pending"
    StatusClaimed   TaskStatus = "claimed"
    StatusRunning   TaskStatus = "running"
    StatusCompleted TaskStatus = "completed"
    StatusFailed    TaskStatus = "failed"
)
```

with:

```go
const (
    StatusPending         TaskStatus = "pending"
    StatusClaimed         TaskStatus = "claimed"
    StatusInProgress      TaskStatus = "in_progress"
    StatusPROpened        TaskStatus = "pr_opened"
    StatusReviewRequested TaskStatus = "review_requested"
    StatusCompleted       TaskStatus = "completed"
    StatusFailed          TaskStatus = "failed"
)
```

- [ ] **Step 2: Replace SQL literals `'running'` with `'in_progress'` and the Go identifier `StatusRunning` with `StatusInProgress` across all source files listed above**

In each of `lifecycle.go`, `logs.go`, `dashboard.go`, `agent_detail.go`, `workers.go`, `tasks.go`, `tasks_update_test.go`, `lifecycle_test.go`, `logs_test.go`, `handlers.go`, `handlers_test.go`, `admin.go`, `dashboard_fragment.html`, `tasks_detail.html`:

- Replace SQL literal `'running'` with `'in_progress'` (note: leave `'pending'`, `'claimed'`, `'completed'`, `'failed'` alone).
- Replace Go identifier `StatusRunning` with `StatusInProgress`.
- In comments and human-readable strings (e.g. `"cannot cancel an active task (claimed or running)"`), change `running` to `in_progress`.

In `internal/mastermind/grpcserver/admin.go`, the `knownStatuses` map at line ~130 becomes:

```go
var knownStatuses = map[string]store.TaskStatus{
    "pending":          store.StatusPending,
    "claimed":          store.StatusClaimed,
    "in_progress":      store.StatusInProgress,
    "pr_opened":        store.StatusPROpened,
    "review_requested": store.StatusReviewRequested,
    "completed":        store.StatusCompleted,
    "failed":           store.StatusFailed,
}
```

In `internal/mastermind/httpserver/handlers.go`, the dashboard `Statuses` slice (search for `store.StatusRunning`) becomes:

```go
Statuses: []store.TaskStatus{
    store.StatusPending,
    store.StatusClaimed,
    store.StatusInProgress,
    store.StatusPROpened,
    store.StatusReviewRequested,
    store.StatusCompleted,
    store.StatusFailed,
},
```

In `internal/mastermind/httpserver/templates/dashboard_fragment.html`, change:

```html
<span class="pill {{ if eq (printf "%s" .CurrentTask.Status) "running" }}filled{{ end }}">
```

to:

```html
<span class="pill {{ if eq (printf "%s" .CurrentTask.Status) "in_progress" }}filled{{ end }}">
```

In `internal/mastermind/httpserver/templates/tasks_detail.html`, change:

```html
{{ if ne .Task.Status "claimed" }}{{ if ne .Task.Status "running" }}
```

to:

```html
{{ if ne .Task.Status "claimed" }}{{ if ne .Task.Status "in_progress" }}
```

In `internal/mastermind/store/tasks_update_test.go`, the test that injects `running` (look for `UPDATE tasks SET status='running'`) becomes `status='in_progress'`.

- [ ] **Step 3: Build the whole module**

Run:

```bash
go build ./...
```

Expected: clean build. Any remaining `StatusRunning` reference becomes a compile error pointing at the file to fix.

- [ ] **Step 4: Run all unit tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS for every package. Any failing test indicates a missed `running` literal or human-readable string.

- [ ] **Step 5: Commit**

```bash
git add internal/ migrations/
git commit -m "refactor(status): rename task_status running to in_progress; add pr_opened/review_requested constants"
```

---

## Stage 2 — `payload.instructions` validation at `CreateTask`

The validator lives in the gRPC handler so HTTP/HTMX path callers (which build `CreateTaskRequest` server-side) and gRPC clients are validated identically.

### Task 2.1: Validator in `admin.go`

**Files:**
- Modify: `internal/mastermind/grpcserver/admin.go`
- Modify: `internal/mastermind/grpcserver/admin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/grpcserver/admin_test.go`:

```go
func TestCreateTask_RequiresInstructions(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)

    cases := []struct{
        name    string
        payload []byte
    }{
        {"missing key", []byte(`{}`)},
        {"empty string", []byte(`{"instructions":""}`)},
        {"whitespace", []byte(`{"instructions":"   "}`)},
        {"wrong type", []byte(`{"instructions":42}`)},
        {"not json", []byte(`not-json`)},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            _, err := a.CreateTask(ctx, &pb.CreateTaskRequest{
                Name:    "t",
                Payload: tc.payload,
            })
            if status.Code(err) != codes.InvalidArgument {
                t.Fatalf("got %v, want InvalidArgument", err)
            }
        })
    }
}

func TestCreateTask_AcceptsValidInstructions(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    resp, err := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"do the thing"}`),
    })
    if err != nil {
        t.Fatalf("CreateTask: %v", err)
    }
    if resp.GetTask().GetId() == "" {
        t.Error("missing id")
    }
}
```

(If imports for `codes`, `status`, `pb`, `store` aren't present in this file, add them following the existing import block style.)

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/mastermind/grpcserver -run 'TestCreateTask_(RequiresInstructions|AcceptsValidInstructions)' -count=1 -v
```

Expected: FAIL — `CreateTask` accepts payloads without `instructions` today.

- [ ] **Step 3: Implement the validator**

In `internal/mastermind/grpcserver/admin.go`, add (above `CreateTask`):

```go
// validateInstructions ensures the create-time payload carries a non-empty
// `instructions` text. The text is the canonical "what should the agent do"
// surface; the rest of the payload remains opaque.
func validateInstructions(payload []byte) error {
    if len(payload) == 0 {
        return errors.New(`payload.instructions must be a non-empty string`)
    }
    var v struct {
        Instructions *string `json:"instructions"`
    }
    if err := json.Unmarshal(payload, &v); err != nil {
        return fmt.Errorf("payload is not valid JSON: %w", err)
    }
    if v.Instructions == nil {
        return errors.New(`payload.instructions must be a non-empty string`)
    }
    if strings.TrimSpace(*v.Instructions) == "" {
        return errors.New(`payload.instructions must be a non-empty string`)
    }
    return nil
}
```

Add `"strings"` and `"fmt"` to the import block if missing.

In `CreateTask`, after the existing JSON well-formedness check, call the validator. The full `payload` block becomes:

```go
var payload json.RawMessage
if len(req.GetPayload()) > 0 {
    payload = json.RawMessage(req.GetPayload())
    var tmp any
    if err := json.Unmarshal(payload, &tmp); err != nil {
        return nil, status.Errorf(codes.InvalidArgument, "payload is not valid JSON: %v", err)
    }
}
if err := validateInstructions(payload); err != nil {
    return nil, status.Error(codes.InvalidArgument, err.Error())
}
```

- [ ] **Step 4: Run the new tests; expect PASS**

```bash
go test ./internal/mastermind/grpcserver -run 'TestCreateTask_(RequiresInstructions|AcceptsValidInstructions)' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Run the full grpcserver test suite — many existing tests will now fail because they create tasks with empty payload**

```bash
go test ./internal/mastermind/grpcserver -count=1
```

Expected: existing tests fail with `InvalidArgument`. This is the next step's work.

- [ ] **Step 6: Update existing test fixtures to include `instructions`**

In every test that calls `a.CreateTask(...)` or `client.CreateTask(...)` without a payload, set:

```go
Payload: []byte(`{"instructions":"test"}`),
```

This affects (search the repo and update each occurrence): `internal/mastermind/grpcserver/admin_test.go`, `internal/mastermind/grpcserver/admin_cli_test.go`, `internal/mastermind/grpcserver/server_test.go`, `internal/mcpserver/tools_test.go`, `internal/cli/cli_test.go`, `internal/e2e/e2e_test.go`. Any test calling the store directly (`s.CreateTask(ctx, store.NewTaskInput{...})`) **does NOT need updating** — the store layer accepts any payload; only the gRPC handler enforces the rule.

- [ ] **Step 7: Run the full test suite to confirm green**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/
git commit -m "feat(admin): require non-empty payload.instructions at CreateTask"
```

---

## Stage 3 — Transition allow-list and store methods

We add new store methods for the four new transitions. Each method is a single conditional `UPDATE`. The allow-list lives in `transitions.go` so tests can iterate it.

### Task 3.1: Transitions allow-list

**Files:**
- Create: `internal/mastermind/store/transitions.go`

- [ ] **Step 1: Write the file**

Create `internal/mastermind/store/transitions.go`:

```go
package store

// role identifies whether a transition is invoked over the worker-facing
// TaskService (a worker holding a claim) or the AdminService (operator).
// Worker transitions additionally require claimed_by to match the caller.
type role int

const (
    roleWorker role = iota + 1
    roleAdmin
)

type transitionKey struct {
    role   role
    action string
}

// allowedFrom is the canonical map of (role, action) -> source states it may
// be invoked from. A store method MUST consult this list when issuing a
// conditional UPDATE; tests assert the map matches the matrix in the spec.
//
// Forward transitions only — admin RetryTask and DeleteTask continue to live
// in tasks.go and have their own enforcement (RequeueTask uses a status-IN
// guard, DeleteTask similarly).
var allowedFrom = map[transitionKey][]TaskStatus{
    {roleWorker, "open_pr"}:        {StatusInProgress},
    {roleWorker, "set_jira_url"}:   {StatusClaimed, StatusInProgress},
    {roleWorker, "complete"}:       {StatusInProgress},
    {roleWorker, "fail"}:           {StatusInProgress},
    {roleAdmin, "request_review"}:  {StatusPROpened},
    {roleAdmin, "finalize"}:        {StatusPROpened, StatusReviewRequested},
    {roleAdmin, "retry"}:           {StatusPending, StatusCompleted, StatusFailed},
}

// allowedFromStrings returns the SQL-friendly string list for use in
// `status = ANY($n)` clauses. Returns nil when key is unknown so callers
// fail closed.
func allowedFromStrings(r role, action string) []string {
    states, ok := allowedFrom[transitionKey{role: r, action: action}]
    if !ok {
        return nil
    }
    out := make([]string, 0, len(states))
    for _, s := range states {
        out = append(out, string(s))
    }
    return out
}
```

- [ ] **Step 2: Build to ensure the package compiles**

```bash
go build ./internal/mastermind/store
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/store/transitions.go
git commit -m "feat(store): add transition allow-list for worker/admin actions"
```

### Task 3.2: `(*Store).SetJiraURL`

**Files:**
- Create: `internal/mastermind/store/jira.go`
- Create: `internal/mastermind/store/jira_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/mastermind/store/jira_test.go`:

```go
package store

import (
    "context"
    "testing"
)

func TestSetJiraURL_AllowedFromClaimedAndInProgress(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")

    // claimed -> set_jira_url
    if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-1"); err != nil {
        t.Fatalf("SetJiraURL from claimed: %v", err)
    }
    got, _ := s.GetTask(ctx, claimed.ID)
    if got.JiraURL == nil || *got.JiraURL != "https://jira/T-1" {
        t.Errorf("jira_url=%v, want https://jira/T-1", got.JiraURL)
    }

    // claimed -> in_progress via heartbeat, then set_jira_url again
    _ = s.Heartbeat(ctx, "w1", claimed.ID)
    if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-2"); err != nil {
        t.Fatalf("SetJiraURL from in_progress: %v", err)
    }
    got, _ = s.GetTask(ctx, claimed.ID)
    if got.JiraURL == nil || *got.JiraURL != "https://jira/T-2" {
        t.Errorf("jira_url=%v, want https://jira/T-2", got.JiraURL)
    }
}

func TestSetJiraURL_RejectsNonOwner(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")

    if err := s.SetJiraURL(ctx, "w2", claimed.ID, "https://jira/T-X"); err != ErrNotOwner {
        t.Errorf("got %v, want ErrNotOwner", err)
    }
}

func TestSetJiraURL_RejectsFromPending(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    created, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})

    if err := s.SetJiraURL(ctx, "w1", created.ID, "https://jira/T-X"); err != ErrNotOwner {
        // pending tasks have no claimed_by, so the owner guard fails first.
        t.Errorf("got %v, want ErrNotOwner", err)
    }
}

func TestSetJiraURL_RefreshesHeartbeat(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")

    before, _ := s.GetTask(ctx, claimed.ID)
    if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-1"); err != nil {
        t.Fatalf("SetJiraURL: %v", err)
    }
    after, _ := s.GetTask(ctx, claimed.ID)
    if before.LastHeartbeatAt == nil || after.LastHeartbeatAt == nil {
        t.Fatal("missing heartbeat timestamps")
    }
    if !after.LastHeartbeatAt.After(*before.LastHeartbeatAt) && !after.LastHeartbeatAt.Equal(*before.LastHeartbeatAt) {
        t.Errorf("heartbeat moved backwards: before=%v after=%v", before.LastHeartbeatAt, after.LastHeartbeatAt)
    }
}
```

- [ ] **Step 2: Run the tests; expect compile error**

```bash
go test ./internal/mastermind/store -run TestSetJiraURL -count=1
```

Expected: FAIL — `s.SetJiraURL undefined`.

- [ ] **Step 3: Implement**

Create `internal/mastermind/store/jira.go`:

```go
package store

import (
    "context"

    "github.com/google/uuid"
)

// SetJiraURL attaches a Jira issue URL to the task the caller currently owns.
// Allowed from claimed and in_progress states. Refreshes the lease (same
// effect as a heartbeat). Returns ErrNotOwner if the caller is not the
// current claim holder or the task is in any other state.
func (s *Store) SetJiraURL(ctx context.Context, workerID string, taskID uuid.UUID, url string) error {
    states := allowedFromStrings(roleWorker, "set_jira_url")
    var urlPtr *string
    if url != "" {
        urlPtr = &url
    }
    ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET jira_url          = $3,
          last_heartbeat_at = now(),
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status = ANY($4::task_status[])
    `, taskID, workerID, urlPtr, states)
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        return ErrNotOwner
    }
    return nil
}
```

- [ ] **Step 4: Run tests; expect PASS**

```bash
go test ./internal/mastermind/store -run TestSetJiraURL -count=1 -v
```

Expected: PASS for all four cases.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/jira.go internal/mastermind/store/jira_test.go
git commit -m "feat(store): add SetJiraURL for claimed/in_progress tasks"
```

### Task 3.3: `(*Store).OpenPR`

**Files:**
- Create: `internal/mastermind/store/open_pr.go`
- Create: `internal/mastermind/store/open_pr_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mastermind/store/open_pr_test.go`:

```go
package store

import (
    "context"
    "testing"
)

func TestOpenPR_HappyPath(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")
    _ = s.Heartbeat(ctx, "w1", claimed.ID) // claimed -> in_progress

    if err := s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1"); err != nil {
        t.Fatalf("OpenPR: %v", err)
    }
    got, _ := s.GetTask(ctx, claimed.ID)
    if got.Status != StatusPROpened {
        t.Errorf("status=%q, want pr_opened", got.Status)
    }
    if got.GithubPRURL == nil || *got.GithubPRURL != "https://github.com/o/r/pull/1" {
        t.Errorf("github_pr_url=%v, want https://github.com/o/r/pull/1", got.GithubPRURL)
    }
    if got.ClaimedBy != nil || got.ClaimedAt != nil || got.LastHeartbeatAt != nil {
        t.Errorf("claim fields not cleared: %+v", got)
    }
}

func TestOpenPR_RejectsFromClaimed(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1") // status=claimed, no heartbeat yet

    if err := s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1"); err != ErrNotOwner {
        t.Errorf("got %v, want ErrNotOwner (claimed isn't a valid source for open_pr)", err)
    }
}

func TestOpenPR_RejectsNonOwner(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")
    _ = s.Heartbeat(ctx, "w1", claimed.ID)

    if err := s.OpenPR(ctx, "w2", claimed.ID, "https://github.com/o/r/pull/1"); err != ErrNotOwner {
        t.Errorf("got %v, want ErrNotOwner", err)
    }
}
```

- [ ] **Step 2: Run; expect compile fail**

```bash
go test ./internal/mastermind/store -run TestOpenPR -count=1
```

Expected: FAIL — `s.OpenPR undefined`.

- [ ] **Step 3: Implement**

Create `internal/mastermind/store/open_pr.go`:

```go
package store

import (
    "context"
    "errors"

    "github.com/google/uuid"
)

// ErrEmptyPRURL is returned by OpenPR when the supplied URL is empty.
var ErrEmptyPRURL = errors.New("task: github_pr_url is required")

// OpenPR atomically transitions the caller's claimed task from in_progress
// to pr_opened: it sets github_pr_url, clears the claim metadata
// (claimed_by, claimed_at, last_heartbeat_at), and updates updated_at.
// The caller's claim is released by this call.
func (s *Store) OpenPR(ctx context.Context, workerID string, taskID uuid.UUID, url string) error {
    if url == "" {
        return ErrEmptyPRURL
    }
    states := allowedFromStrings(roleWorker, "open_pr")
    ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status            = 'pr_opened',
          github_pr_url     = $3,
          claimed_by        = NULL,
          claimed_at        = NULL,
          last_heartbeat_at = NULL,
          updated_at        = now()
      WHERE id = $1 AND claimed_by = $2 AND status = ANY($4::task_status[])
    `, taskID, workerID, url, states)
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        return ErrNotOwner
    }
    return nil
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mastermind/store -run TestOpenPR -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/open_pr.go internal/mastermind/store/open_pr_test.go
git commit -m "feat(store): add OpenPR for atomic in_progress -> pr_opened transition"
```

### Task 3.4: `(*Store).RequestReview` and `(*Store).FinalizeTask`

**Files:**
- Create: `internal/mastermind/store/finalize.go`
- Create: `internal/mastermind/store/finalize_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mastermind/store/finalize_test.go`:

```go
package store

import (
    "context"
    "testing"
)

func openedPR(t *testing.T, s *Store, ctx context.Context, worker string) Task {
    t.Helper()
    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, worker)
    _ = s.Heartbeat(ctx, worker, claimed.ID)
    if err := s.OpenPR(ctx, worker, claimed.ID, "https://github.com/o/r/pull/1"); err != nil {
        t.Fatalf("OpenPR: %v", err)
    }
    got, _ := s.GetTask(ctx, claimed.ID)
    return got
}

func TestRequestReview_HappyPath(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    parked := openedPR(t, s, ctx, "w1")

    if err := s.RequestReview(ctx, parked.ID); err != nil {
        t.Fatalf("RequestReview: %v", err)
    }
    got, _ := s.GetTask(ctx, parked.ID)
    if got.Status != StatusReviewRequested {
        t.Errorf("status=%q, want review_requested", got.Status)
    }
}

func TestRequestReview_RejectsFromInProgress(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")
    _ = s.Heartbeat(ctx, "w1", claimed.ID)

    if err := s.RequestReview(ctx, claimed.ID); err != ErrInvalidTransition {
        t.Errorf("got %v, want ErrInvalidTransition", err)
    }
}

func TestFinalizeTask_SuccessFromPROpened(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    parked := openedPR(t, s, ctx, "w1")

    if err := s.FinalizeTask(ctx, parked.ID, true, ""); err != nil {
        t.Fatalf("FinalizeTask: %v", err)
    }
    got, _ := s.GetTask(ctx, parked.ID)
    if got.Status != StatusCompleted {
        t.Errorf("status=%q, want completed", got.Status)
    }
    if got.CompletedAt == nil {
        t.Error("completed_at not set")
    }
    if got.GithubPRURL == nil {
        t.Error("github_pr_url should be preserved on success")
    }
}

func TestFinalizeTask_FailureFromReviewRequested(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    parked := openedPR(t, s, ctx, "w1")
    _ = s.RequestReview(ctx, parked.ID)

    if err := s.FinalizeTask(ctx, parked.ID, false, "rejected"); err != nil {
        t.Fatalf("FinalizeTask: %v", err)
    }
    got, _ := s.GetTask(ctx, parked.ID)
    if got.Status != StatusFailed {
        t.Errorf("status=%q, want failed", got.Status)
    }
    if got.LastError == nil || *got.LastError != "rejected" {
        t.Errorf("last_error=%v, want rejected", got.LastError)
    }
}

func TestFinalizeTask_RejectsFromInProgress(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")
    _ = s.Heartbeat(ctx, "w1", claimed.ID)

    if err := s.FinalizeTask(ctx, claimed.ID, true, ""); err != ErrInvalidTransition {
        t.Errorf("got %v, want ErrInvalidTransition", err)
    }
}
```

- [ ] **Step 2: Run; expect compile fail**

```bash
go test ./internal/mastermind/store -run 'TestRequestReview|TestFinalizeTask' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement**

Create `internal/mastermind/store/finalize.go`:

```go
package store

import (
    "context"
    "errors"

    "github.com/google/uuid"
)

// ErrInvalidTransition is returned by admin-driven transition methods when
// the task is in a state that does not allow the requested action.
var ErrInvalidTransition = errors.New("task: invalid transition for current state")

// RequestReview transitions a parked pr_opened task to review_requested.
// Returns ErrInvalidTransition if the task is in any other state, and
// ErrNotFound if the id is unknown.
func (s *Store) RequestReview(ctx context.Context, id uuid.UUID) error {
    states := allowedFromStrings(roleAdmin, "request_review")
    ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status     = 'review_requested',
          updated_at = now()
      WHERE id = $1 AND status = ANY($2::task_status[])
    `, id, states)
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
            return ErrNotFound
        }
        return ErrInvalidTransition
    }
    return nil
}

// FinalizeTask terminates a parked task. From pr_opened or review_requested
// only: success transitions to completed, failure to failed (terminal — no
// retry from these states). Always terminal; attempt_count is not changed.
func (s *Store) FinalizeTask(ctx context.Context, id uuid.UUID, success bool, errMsg string) error {
    states := allowedFromStrings(roleAdmin, "finalize")
    var (
        targetStatus TaskStatus
        errPtr       *string
    )
    if success {
        targetStatus = StatusCompleted
    } else {
        targetStatus = StatusFailed
        if errMsg != "" {
            errPtr = &errMsg
        }
    }
    ct, err := s.pool.Exec(ctx, `
      UPDATE tasks
      SET status       = $2,
          completed_at = CASE WHEN $2 = 'completed'::task_status THEN now() ELSE completed_at END,
          last_error   = COALESCE($3, last_error),
          updated_at   = now()
      WHERE id = $1 AND status = ANY($4::task_status[])
    `, id, targetStatus, errPtr, states)
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
            return ErrNotFound
        }
        return ErrInvalidTransition
    }
    return nil
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mastermind/store -run 'TestRequestReview|TestFinalizeTask' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/finalize.go internal/mastermind/store/finalize_test.go
git commit -m "feat(store): add RequestReview and FinalizeTask for parked tasks"
```

### Task 3.5: Tighten `RequeueTask` (clear `github_pr_url`, reject parked)

**Files:**
- Modify: `internal/mastermind/store/tasks.go`
- Modify: `internal/mastermind/store/tasks_update_test.go` (or a new `requeue_test.go`)

- [ ] **Step 1: Write the failing tests**

Append to `internal/mastermind/store/tasks_update_test.go` (or create `internal/mastermind/store/requeue_test.go`):

```go
func TestRequeueTask_ClearsGithubPRURL(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    pr := "https://github.com/o/r/pull/1"
    _, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, GithubPRURL: &pr, JiraURL: strPtr("https://jira/T-1")})
    // Force the task into 'failed' so RequeueTask will accept it.
    if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='failed'`); err != nil {
        t.Fatal(err)
    }
    list, err := s.ListTasks(ctx, ListTasksFilter{})
    if err != nil { t.Fatal(err) }
    id := list.Items[0].ID

    out, err := s.RequeueTask(ctx, id)
    if err != nil { t.Fatalf("RequeueTask: %v", err) }
    if out.GithubPRURL != nil {
        t.Errorf("github_pr_url=%v, want nil", out.GithubPRURL)
    }
    if out.JiraURL == nil || *out.JiraURL != "https://jira/T-1" {
        t.Errorf("jira_url=%v, want preserved", out.JiraURL)
    }
}

func TestRequeueTask_RejectsFromPROpened(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    parked := openedPR(t, s, ctx, "w1") // helper from finalize_test.go

    _, err := s.RequeueTask(ctx, parked.ID)
    if err != ErrNotRequeueable {
        t.Errorf("got %v, want ErrNotRequeueable", err)
    }
}

func TestRequeueTask_RejectsFromReviewRequested(t *testing.T) {
    ctx := context.Background()
    s, _ := Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.pool)

    parked := openedPR(t, s, ctx, "w1")
    _ = s.RequestReview(ctx, parked.ID)

    _, err := s.RequeueTask(ctx, parked.ID)
    if err != ErrNotRequeueable {
        t.Errorf("got %v, want ErrNotRequeueable", err)
    }
}

func strPtr(s string) *string { return &s }
```

(If `strPtr` already exists in the package, omit the helper.)

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/mastermind/store -run TestRequeueTask -count=1 -v
```

Expected: FAIL on `ClearsGithubPRURL` (today's `RequeueTask` does not clear it) and on the parked rejection cases (today's `RequeueTask` accepts only `pending|completed|failed`; the parked states are already excluded — these tests confirm and lock that behavior).

If both rejection tests already pass because the existing IN-clause excludes parked states, leave them as regression tests.

- [ ] **Step 3: Update `RequeueTask` in `internal/mastermind/store/tasks.go`**

Modify the `UPDATE` to clear `github_pr_url`:

```go
func (s *Store) RequeueTask(ctx context.Context, id uuid.UUID) (Task, error) {
    row := s.pool.QueryRow(ctx, `
      UPDATE tasks
      SET status            = 'pending',
          claimed_by        = NULL,
          claimed_at        = NULL,
          last_heartbeat_at = NULL,
          completed_at      = NULL,
          last_error        = NULL,
          progress_note     = NULL,
          github_pr_url     = NULL,
          attempt_count     = 0,
          scheduled_for     = NULL,
          updated_at        = now()
      WHERE id = $1 AND status IN ('pending','completed','failed')
      RETURNING `+taskColumns, id)
    t, err := scanTask(row)
    if errors.Is(err, pgx.ErrNoRows) {
        if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
            return Task{}, ErrNotFound
        }
        return Task{}, ErrNotRequeueable
    }
    return t, err
}
```

(The status-IN guard already excludes the parked states, so the rejection tests should pass without further code changes.)

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mastermind/store -run TestRequeueTask -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/tasks.go internal/mastermind/store/tasks_update_test.go
git commit -m "feat(store): clear github_pr_url on RequeueTask; lock rejection of parked states"
```

### Task 3.6: Reaper regression test for parked-state exclusion

**Files:**
- Modify: `internal/mastermind/reaper/reaper_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/reaper/reaper_test.go` a focused test that creates a task, drives it to `pr_opened`, manually backdates `last_heartbeat_at`, calls `s.ReapStale`, and asserts the task is unchanged.

```go
func TestReapStale_DoesNotReapParkedStates(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    _, _ = s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3})
    claimed, _ := s.ClaimTask(ctx, "w1")
    _ = s.Heartbeat(ctx, "w1", claimed.ID)
    _ = s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1")

    // Backdate last_heartbeat_at to a time well past any visibility timeout.
    // Even though OpenPR cleared it, force a stale value to prove the reaper's
    // status filter is what guards parked tasks, not the NULL heartbeat alone.
    if _, err := s.Pool().Exec(ctx,
        `UPDATE tasks SET last_heartbeat_at = now() - interval '1 hour' WHERE id = $1`,
        claimed.ID,
    ); err != nil {
        t.Fatal(err)
    }

    out, err := s.ReapStale(ctx, time.Second)
    if err != nil { t.Fatalf("ReapStale: %v", err) }
    if out.Requeued != 0 || out.Failed != 0 {
        t.Errorf("reaped a parked task: %+v", out)
    }
    got, _ := s.GetTask(ctx, claimed.ID)
    if got.Status != store.StatusPROpened {
        t.Errorf("status=%q, want pr_opened", got.Status)
    }
}
```

(Adapt imports and helpers — `truncate`, `testDSN` — to whatever the existing reaper test file uses.)

- [ ] **Step 2: Run; expect PASS (this is a regression test against Stage 1's index update)**

```bash
go test ./internal/mastermind/reaper -run TestReapStale_DoesNotReapParkedStates -count=1 -v
```

Expected: PASS, given that Stage 1 already updated the `ReapStale` SQL filter to `('claimed','in_progress')`.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/reaper/reaper_test.go
git commit -m "test(reaper): regression — parked tasks are not reaped despite stale heartbeat"
```

---

## Stage 4 — Proto and gRPC handlers

### Task 4.1: Edit `tasks.proto` and regenerate

**Files:**
- Modify: `internal/proto/tasks.proto`
- Modify (regenerated): `internal/proto/tasks.pb.go`, `internal/proto/tasks_grpc.pb.go`

- [ ] **Step 1: Add new RPCs and messages to `internal/proto/tasks.proto`**

Inside `service TaskService`, after `AppendLog`, add:

```proto
  // SetJiraURL attaches a JIRA issue URL to the caller's claimed task.
  // Allowed from `claimed` or `in_progress`. Refreshes the lease.
  // Returns FAILED_PRECONDITION when the caller no longer owns the claim.
  rpc SetJiraURL(SetJiraURLRequest) returns (SetJiraURLResponse);

  // OpenPR atomically transitions the caller's `in_progress` task to
  // `pr_opened`, sets github_pr_url, and releases the claim. Returns
  // INVALID_ARGUMENT when github_pr_url is empty and FAILED_PRECONDITION
  // when the caller no longer owns the claim or the task is not in
  // `in_progress`.
  rpc OpenPR(OpenPRRequest) returns (OpenPRResponse);
```

Append the corresponding messages (anywhere after `AppendLogResponse`):

```proto
message SetJiraURLRequest  {
  string worker_id = 1;
  string task_id   = 2;
  string url       = 3;
}
message SetJiraURLResponse {}

message OpenPRRequest {
  string worker_id     = 1;
  string task_id       = 2;
  string github_pr_url = 3;
}
message OpenPRResponse {}
```

Inside `service AdminService`, after `RetryTask`, add:

```proto
  // RequestReview transitions a `pr_opened` task to `review_requested`.
  // FAILED_PRECONDITION from any other state.
  rpc RequestReview(RequestReviewRequest) returns (RequestReviewResponse);

  // FinalizeTask terminates a parked task with success or failure. Allowed
  // only from `pr_opened` or `review_requested`. `outcome` is a oneof so
  // "neither set" / "both set" are unrepresentable.
  rpc FinalizeTask(FinalizeTaskRequest) returns (FinalizeTaskResponse);
```

And the messages:

```proto
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

- [ ] **Step 2: Regenerate**

```bash
make proto
```

Expected: `internal/proto/tasks.pb.go` and `internal/proto/tasks_grpc.pb.go` are updated. The repo must have `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` installed (per existing repo setup).

- [ ] **Step 3: Build to confirm code generation succeeded**

```bash
go build ./...
```

Expected: clean build (handlers don't yet implement the new methods, but `UnimplementedTaskServiceServer` and `UnimplementedAdminServiceServer` provide stubs).

- [ ] **Step 4: Commit**

```bash
git add internal/proto/
git commit -m "feat(proto): add SetJiraURL/OpenPR (TaskService) and RequestReview/FinalizeTask (AdminService)"
```

### Task 4.2: TaskService handlers — `SetJiraURL`, `OpenPR`

**Files:**
- Modify: `internal/mastermind/grpcserver/server.go`
- Modify: `internal/mastermind/grpcserver/server_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/grpcserver/server_test.go` (or create `server_pr_test.go` if the file is large):

```go
func TestSetJiraURL_HappyPath(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)

    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    claim, _ := srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _ = claim
    if _, err := srv.SetJiraURL(ctx, &pb.SetJiraURLRequest{
        WorkerId: "w1",
        TaskId:   created.GetTask().GetId(),
        Url:      "https://jira/T-1",
    }); err != nil {
        t.Fatalf("SetJiraURL: %v", err)
    }
}

func TestSetJiraURL_NotOwnerReturnsFailedPrecondition(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})

    _, err := srv.SetJiraURL(ctx, &pb.SetJiraURLRequest{
        WorkerId: "w2",
        TaskId:   created.GetTask().GetId(),
        Url:      "https://jira/T-1",
    })
    if status.Code(err) != codes.FailedPrecondition {
        t.Errorf("got %v, want FailedPrecondition", err)
    }
}

func TestOpenPR_HappyPath(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = srv.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})

    _, err := srv.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId:    "w1",
        TaskId:      created.GetTask().GetId(),
        GithubPrUrl: "https://github.com/o/r/pull/1",
    })
    if err != nil { t.Fatalf("OpenPR: %v", err) }
}

func TestOpenPR_EmptyURLReturnsInvalidArgument(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    srv := grpcserver.New(s)
    _, err := srv.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId:    "w1",
        TaskId:      uuid.New().String(),
        GithubPrUrl: "",
    })
    if status.Code(err) != codes.InvalidArgument {
        t.Errorf("got %v, want InvalidArgument", err)
    }
}
```

- [ ] **Step 2: Run; expect FAIL (handler missing)**

```bash
go test ./internal/mastermind/grpcserver -run 'TestSetJiraURL_|TestOpenPR_' -count=1
```

Expected: FAIL — the unimplemented handler returns `codes.Unimplemented`.

- [ ] **Step 3: Implement both handlers in `server.go`**

Append to `internal/mastermind/grpcserver/server.go`:

```go
func (s *Server) SetJiraURL(ctx context.Context, req *pb.SetJiraURLRequest) (*pb.SetJiraURLResponse, error) {
    if req.GetWorkerId() == "" {
        return nil, status.Error(codes.InvalidArgument, "worker_id is required")
    }
    id, err := uuid.Parse(req.GetTaskId())
    if err != nil {
        return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
    }
    if err := s.store.SetJiraURL(ctx, req.GetWorkerId(), id, req.GetUrl()); err != nil {
        if errors.Is(err, store.ErrNotOwner) {
            return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
        }
        return nil, status.Errorf(codes.Internal, "set_jira_url: %v", err)
    }
    return &pb.SetJiraURLResponse{}, nil
}

func (s *Server) OpenPR(ctx context.Context, req *pb.OpenPRRequest) (*pb.OpenPRResponse, error) {
    if req.GetWorkerId() == "" {
        return nil, status.Error(codes.InvalidArgument, "worker_id is required")
    }
    if req.GetGithubPrUrl() == "" {
        return nil, status.Error(codes.InvalidArgument, "github_pr_url is required")
    }
    id, err := uuid.Parse(req.GetTaskId())
    if err != nil {
        return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
    }
    if err := s.store.OpenPR(ctx, req.GetWorkerId(), id, req.GetGithubPrUrl()); err != nil {
        if errors.Is(err, store.ErrNotOwner) {
            return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task or task not in_progress")
        }
        if errors.Is(err, store.ErrEmptyPRURL) {
            return nil, status.Error(codes.InvalidArgument, "github_pr_url is required")
        }
        return nil, status.Errorf(codes.Internal, "open_pr: %v", err)
    }
    return &pb.OpenPRResponse{}, nil
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mastermind/grpcserver -run 'TestSetJiraURL_|TestOpenPR_' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/grpcserver/server.go internal/mastermind/grpcserver/server_test.go
git commit -m "feat(grpc): TaskService SetJiraURL and OpenPR handlers"
```

### Task 4.3: AdminService handlers — `RequestReview`, `FinalizeTask`

**Files:**
- Modify: `internal/mastermind/grpcserver/admin.go`
- Modify: `internal/mastermind/grpcserver/admin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/grpcserver/admin_test.go`:

```go
func TestRequestReview_HappyPath(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = srv.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = srv.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1",
    })

    resp, err := a.RequestReview(ctx, &pb.RequestReviewRequest{Id: created.GetTask().GetId()})
    if err != nil { t.Fatalf("RequestReview: %v", err) }
    if resp.GetTask().GetStatus() != "review_requested" {
        t.Errorf("status=%q, want review_requested", resp.GetTask().GetStatus())
    }
}

func TestRequestReview_RejectsFromInProgress(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = srv.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})

    _, err := a.RequestReview(ctx, &pb.RequestReviewRequest{Id: created.GetTask().GetId()})
    if status.Code(err) != codes.FailedPrecondition {
        t.Errorf("got %v, want FailedPrecondition", err)
    }
}

func TestFinalizeTask_Success(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = srv.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = srv.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1",
    })

    resp, err := a.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
        Id: created.GetTask().GetId(),
        Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
    })
    if err != nil { t.Fatalf("FinalizeTask: %v", err) }
    if resp.GetTask().GetStatus() != "completed" {
        t.Errorf("status=%q, want completed", resp.GetTask().GetStatus())
    }
}

func TestFinalizeTask_Failure(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    srv := grpcserver.New(s)
    created, _ := a.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "t",
        Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = srv.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = srv.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = srv.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1",
    })

    resp, err := a.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
        Id: created.GetTask().GetId(),
        Outcome: &pb.FinalizeTaskRequest_Failure_{Failure: &pb.FinalizeTaskRequest_Failure{Message: "rejected"}},
    })
    if err != nil { t.Fatalf("FinalizeTask: %v", err) }
    if resp.GetTask().GetStatus() != "failed" {
        t.Errorf("status=%q, want failed", resp.GetTask().GetStatus())
    }
    if resp.GetTask().GetLastError() != "rejected" {
        t.Errorf("last_error=%q, want rejected", resp.GetTask().GetLastError())
    }
}

func TestFinalizeTask_RequiresOutcome(t *testing.T) {
    ctx := context.Background()
    s, _ := store.Open(ctx, testDSN)
    defer s.Close()
    truncate(t, s.Pool())

    a := grpcserver.NewAdmin(s)
    _, err := a.FinalizeTask(ctx, &pb.FinalizeTaskRequest{Id: uuid.New().String()})
    if status.Code(err) != codes.InvalidArgument {
        t.Errorf("got %v, want InvalidArgument", err)
    }
}
```

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/mastermind/grpcserver -run 'TestRequestReview_|TestFinalizeTask_' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `internal/mastermind/grpcserver/admin.go`:

```go
func (a *AdminServer) RequestReview(ctx context.Context, req *pb.RequestReviewRequest) (*pb.RequestReviewResponse, error) {
    id, err := uuid.Parse(req.GetId())
    if err != nil {
        return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
    }
    switch err := a.store.RequestReview(ctx, id); {
    case err == nil:
        t, gerr := a.store.GetTask(ctx, id)
        if gerr != nil {
            return nil, status.Errorf(codes.Internal, "get after request_review: %v", gerr)
        }
        return &pb.RequestReviewResponse{Task: toTaskDetail(t)}, nil
    case errors.Is(err, store.ErrNotFound):
        return nil, status.Errorf(codes.NotFound, "task %s not found", id)
    case errors.Is(err, store.ErrInvalidTransition):
        return nil, status.Error(codes.FailedPrecondition, "task is not in pr_opened")
    default:
        return nil, status.Errorf(codes.Internal, "request_review: %v", err)
    }
}

func (a *AdminServer) FinalizeTask(ctx context.Context, req *pb.FinalizeTaskRequest) (*pb.FinalizeTaskResponse, error) {
    id, err := uuid.Parse(req.GetId())
    if err != nil {
        return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
    }
    var (
        success bool
        errMsg  string
    )
    switch outcome := req.GetOutcome().(type) {
    case *pb.FinalizeTaskRequest_Success_:
        success = true
    case *pb.FinalizeTaskRequest_Failure_:
        success = false
        errMsg = outcome.Failure.GetMessage()
    default:
        return nil, status.Error(codes.InvalidArgument, "outcome is required (success or failure)")
    }
    switch err := a.store.FinalizeTask(ctx, id, success, errMsg); {
    case err == nil:
        t, gerr := a.store.GetTask(ctx, id)
        if gerr != nil {
            return nil, status.Errorf(codes.Internal, "get after finalize: %v", gerr)
        }
        return &pb.FinalizeTaskResponse{Task: toTaskDetail(t)}, nil
    case errors.Is(err, store.ErrNotFound):
        return nil, status.Errorf(codes.NotFound, "task %s not found", id)
    case errors.Is(err, store.ErrInvalidTransition):
        return nil, status.Error(codes.FailedPrecondition, "task is not in pr_opened or review_requested")
    default:
        return nil, status.Errorf(codes.Internal, "finalize: %v", err)
    }
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mastermind/grpcserver -run 'TestRequestReview_|TestFinalizeTask_' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/grpcserver/admin.go internal/mastermind/grpcserver/admin_test.go
git commit -m "feat(grpc): AdminService RequestReview and FinalizeTask handlers"
```

---

## Stage 5 — Worker side (taskclient and MCP)

### Task 5.1: `taskclient` methods

**Files:**
- Modify: `internal/worker/taskclient/taskclient.go`
- Modify: `internal/worker/taskclient/taskclient_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/worker/taskclient/taskclient_test.go`:

```go
func TestTask_SetJiraURL(t *testing.T) {
    fake := &fakeRPC{} // existing fake from this file
    c := taskclient.NewClient(fake, "w1")
    task := taskclient.NewTaskForTest(c, "id-1", "n", nil) // helper added below

    if err := task.SetJiraURL(context.Background(), "https://jira/T-1"); err != nil {
        t.Fatalf("SetJiraURL: %v", err)
    }
    if fake.lastSetJira == nil ||
        fake.lastSetJira.GetWorkerId() != "w1" ||
        fake.lastSetJira.GetTaskId() != "id-1" ||
        fake.lastSetJira.GetUrl() != "https://jira/T-1" {
        t.Errorf("unexpected SetJiraURL request: %+v", fake.lastSetJira)
    }
}

func TestTask_OpenPR(t *testing.T) {
    fake := &fakeRPC{}
    c := taskclient.NewClient(fake, "w1")
    task := taskclient.NewTaskForTest(c, "id-1", "n", nil)

    if err := task.OpenPR(context.Background(), "https://github.com/o/r/pull/1"); err != nil {
        t.Fatalf("OpenPR: %v", err)
    }
    if fake.lastOpenPR == nil ||
        fake.lastOpenPR.GetGithubPrUrl() != "https://github.com/o/r/pull/1" {
        t.Errorf("unexpected OpenPR request: %+v", fake.lastOpenPR)
    }
}
```

(The existing test file already has a `fakeRPC` type. Inspect it; add `lastSetJira` and `lastOpenPR` fields and matching method stubs to satisfy `pb.TaskServiceClient`. If the existing tests construct `*Task` directly without an exported helper, add a small `NewTaskForTest` constructor in `taskclient.go` like other test-only helpers in this repo, or change the new tests to first call `Claim`.)

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/worker/taskclient -run 'TestTask_SetJiraURL|TestTask_OpenPR' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement on `Task`**

Append to `internal/worker/taskclient/taskclient.go` after `AppendLog`:

```go
// SetJiraURL attaches a JIRA URL to the task and refreshes the lease.
func (t *Task) SetJiraURL(ctx context.Context, url string) error {
    _, err := t.rpc.SetJiraURL(ctx, &pb.SetJiraURLRequest{
        WorkerId: t.workerID,
        TaskId:   t.id,
        Url:      url,
    })
    return err
}

// OpenPR atomically transitions the task to pr_opened, sets github_pr_url,
// and releases the caller's claim. After this call the Task is "finalized"
// in the same sense as Complete/Fail: subsequent lifecycle calls return
// ErrAlreadyFinalized. The auto-heartbeat (if running) is stopped.
func (t *Task) OpenPR(ctx context.Context, githubPRURL string) error {
    if err := t.markFinalized(); err != nil {
        return err
    }
    _, err := t.rpc.OpenPR(ctx, &pb.OpenPRRequest{
        WorkerId:    t.workerID,
        TaskId:      t.id,
        GithubPrUrl: githubPRURL,
    })
    return err
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/worker/taskclient -run 'TestTask_SetJiraURL|TestTask_OpenPR' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/taskclient/
git commit -m "feat(taskclient): add Task.SetJiraURL and Task.OpenPR"
```

### Task 5.2: Worker `mcpserver.State` methods

**Files:**
- Modify: `internal/worker/mcpserver/state.go`
- Modify: `internal/worker/mcpserver/state_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/worker/mcpserver/state_test.go`:

```go
func TestState_SetJiraURL_DelegatesToTask(t *testing.T) {
    // Use the existing fakeRPC + State setup from the file.
    rpc := &fakeRPC{}
    cl := taskclient.NewClient(rpc, "w1")
    st := mcpserver.New(cl, time.Hour, slogTestLogger(t))

    rpc.claim = &pb.Task{Id: uuid.New().String(), Name: "t"}
    if _, err := st.ClaimNext(context.Background()); err != nil {
        t.Fatalf("ClaimNext: %v", err)
    }
    if err := st.SetJiraURL(context.Background(), "", "https://jira/T-1"); err != nil {
        t.Fatalf("SetJiraURL: %v", err)
    }
    if rpc.lastSetJira == nil || rpc.lastSetJira.GetUrl() != "https://jira/T-1" {
        t.Errorf("unexpected SetJiraURL request: %+v", rpc.lastSetJira)
    }
}

func TestState_OpenPR_ClearsCurrent(t *testing.T) {
    rpc := &fakeRPC{}
    cl := taskclient.NewClient(rpc, "w1")
    st := mcpserver.New(cl, time.Hour, slogTestLogger(t))

    rpc.claim = &pb.Task{Id: uuid.New().String(), Name: "t"}
    _, _ = st.ClaimNext(context.Background())

    if err := st.OpenPR(context.Background(), "", "https://github.com/o/r/pull/1"); err != nil {
        t.Fatalf("OpenPR: %v", err)
    }
    if _, err := st.Current(); err != mcpserver.ErrNoCurrentTask {
        t.Errorf("Current after OpenPR: got %v, want ErrNoCurrentTask", err)
    }
}
```

(`slogTestLogger` is the existing helper in `state_test.go`. If the existing fake doesn't expose `lastSetJira`/`lastOpenPR`, copy the additions from Task 5.1.)

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/worker/mcpserver -run 'TestState_SetJiraURL|TestState_OpenPR' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `internal/worker/mcpserver/state.go`:

```go
// SetJiraURL attaches a JIRA URL to the current task. The taskID must match
// the current claim (or be empty to refer to it implicitly). Returns
// ErrNoCurrentTask when idle and ErrTaskNotMatching when the id doesn't
// match.
func (s *State) SetJiraURL(ctx context.Context, taskID, url string) error {
    t, err := s.requireTask(taskID)
    if err != nil {
        return err
    }
    return t.SetJiraURL(ctx, url)
}

// OpenPR drives the worker's claimed task through the in_progress ->
// pr_opened transition: it sets github_pr_url and releases the claim.
// On success the local State is cleared (mirroring Complete/Fail) so the
// agent may immediately claim_next_task again.
func (s *State) OpenPR(ctx context.Context, taskID, url string) error {
    t, err := s.requireTask(taskID)
    if err != nil {
        return err
    }
    if err := t.OpenPR(ctx, url); err != nil {
        return err
    }
    s.clear()
    return nil
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/worker/mcpserver -run 'TestState_SetJiraURL|TestState_OpenPR' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/mcpserver/state.go internal/worker/mcpserver/state_test.go
git commit -m "feat(worker-mcp): add State.SetJiraURL and State.OpenPR"
```

### Task 5.3: Worker MCP tools — `set_jira_url`, `open_pr`, expanded `TaskView`

**Files:**
- Modify: `internal/worker/mcpserver/tools.go`
- Modify: `internal/worker/mcpserver/tools_test.go`

- [ ] **Step 1: Expand the `TaskView` to surface `instructions`, `jira_url`, `github_pr_url`**

Replace the existing `TaskView` struct with:

```go
type TaskView struct {
    ID           string `json:"id"`
    Name         string `json:"name"`
    WorkerID     string `json:"worker_id"`
    Instructions string `json:"instructions"`
    Payload      any    `json:"payload"`
    JiraURL      string `json:"jira_url,omitempty"`
    GithubPRURL  string `json:"github_pr_url,omitempty"`
}
```

Update `viewFromTask` to populate `Instructions` from `payload.instructions` when present. The full helper:

```go
func viewFromTask(t *taskclient.Task, workerID string) TaskView {
    v := TaskView{
        ID:       t.ID(),
        Name:     t.Name(),
        WorkerID: workerID,
    }
    raw := t.Payload()
    if len(raw) == 0 {
        v.Payload = map[string]any{}
    } else if err := json.Unmarshal(raw, &v.Payload); err != nil {
        v.Payload = string(raw)
    } else if m, ok := v.Payload.(map[string]any); ok {
        if s, ok := m["instructions"].(string); ok {
            v.Instructions = s
        }
    }
    return v
}
```

(Note: the `Task` returned by `Claim` does not carry `jira_url`/`github_pr_url` — those live on the admin TaskDetail. The view fields are populated only when set later by the agent through tool calls — for the worker's own task they're informational. If you want them surfaced on `get_current_task` too, fetch from a future store-side method; for this iteration, leaving them empty for the worker view is acceptable.)

- [ ] **Step 2: Write the failing test for `set_jira_url`**

Append to `internal/worker/mcpserver/tools_test.go`:

```go
func TestSetJiraURL_Tool(t *testing.T) {
    rpc := &fakeRPC{}
    rpc.claim = &pb.Task{Id: uuid.New().String(), Name: "t"}
    st := mcpserver.New(taskclient.NewClient(rpc, "w1"), time.Hour, slogTestLogger(t))
    _, _ = st.ClaimNext(context.Background())

    s := mcpserver.NewServer(st)
    res, err := callTool(t, s, "set_jira_url", map[string]any{"url": "https://jira/T-1"})
    if err != nil { t.Fatalf("call: %v", err) }
    if res.IsError {
        t.Errorf("set_jira_url returned tool error: %s", textOf(res))
    }
    if rpc.lastSetJira == nil || rpc.lastSetJira.GetUrl() != "https://jira/T-1" {
        t.Errorf("unexpected SetJiraURL: %+v", rpc.lastSetJira)
    }
}

func TestOpenPR_Tool_RequiresURL(t *testing.T) {
    rpc := &fakeRPC{}
    rpc.claim = &pb.Task{Id: uuid.New().String(), Name: "t"}
    st := mcpserver.New(taskclient.NewClient(rpc, "w1"), time.Hour, slogTestLogger(t))
    _, _ = st.ClaimNext(context.Background())

    s := mcpserver.NewServer(st)
    res, _ := callTool(t, s, "open_pr", map[string]any{"github_pr_url": ""})
    if !res.IsError {
        t.Error("expected tool error for empty url")
    }
}

func TestOpenPR_Tool_HappyPathClearsCurrent(t *testing.T) {
    rpc := &fakeRPC{}
    rpc.claim = &pb.Task{Id: uuid.New().String(), Name: "t"}
    st := mcpserver.New(taskclient.NewClient(rpc, "w1"), time.Hour, slogTestLogger(t))
    _, _ = st.ClaimNext(context.Background())

    s := mcpserver.NewServer(st)
    res, err := callTool(t, s, "open_pr", map[string]any{"github_pr_url": "https://github.com/o/r/pull/1"})
    if err != nil || res.IsError {
        t.Fatalf("open_pr: err=%v, isErr=%v, body=%s", err, res.IsError, textOf(res))
    }
    if _, err := st.Current(); err != mcpserver.ErrNoCurrentTask {
        t.Errorf("Current: got %v, want ErrNoCurrentTask", err)
    }
}
```

(`callTool`, `textOf`, and `slogTestLogger` are existing helpers in this file. Add `lastSetJira` / `lastOpenPR` to `fakeRPC` if missing.)

- [ ] **Step 3: Run; expect FAIL**

```bash
go test ./internal/worker/mcpserver -run 'TestSetJiraURL_Tool|TestOpenPR_Tool' -count=1
```

Expected: FAIL — tools don't exist yet.

- [ ] **Step 4: Implement the tools**

In `internal/worker/mcpserver/tools.go`, register the new tools in `NewServer`:

```go
func NewServer(st *State) *mcp.Server {
    s := mcp.NewServer(&mcp.Implementation{Name: "worker-mcp", Version: "v1.0.0"}, nil)
    registerClaimNext(s, st)
    registerGetCurrent(s, st)
    registerUpdateProgress(s, st)
    registerAppendLog(s, st)
    registerSetJiraURL(s, st)
    registerOpenPR(s, st)
    registerCompleteTask(s, st)
    registerFailTask(s, st)
    return s
}
```

Add the two registrations (place near `registerCompleteTask`):

```go
type setJiraURLInput struct {
    TaskID string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
    URL    string `json:"url" jsonschema:"JIRA issue URL (required, non-empty)"`
}

func registerSetJiraURL(s *mcp.Server, st *State) {
    mcp.AddTool(s, &mcp.Tool{
        Name: "set_jira_url",
        Description: "Attach a JIRA issue URL to the worker's claimed task. " +
            "Allowed any time the worker holds the claim (claimed or in_progress). " +
            "Refreshes the lease as a side effect.",
    }, func(ctx context.Context, _ *mcp.CallToolRequest, in setJiraURLInput) (*mcp.CallToolResult, struct{}, error) {
        if in.URL == "" {
            return &mcp.CallToolResult{
                IsError: true,
                Content: []mcp.Content{&mcp.TextContent{Text: "set_jira_url: url is required"}},
            }, struct{}{}, nil
        }
        if err := st.SetJiraURL(ctx, in.TaskID, in.URL); err != nil {
            return toolErr(err, "set_jira_url"), struct{}{}, nil
        }
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: "jira_url set"}},
        }, struct{}{}, nil
    })
}

type openPRInput struct {
    TaskID      string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
    GithubPRURL string `json:"github_pr_url" jsonschema:"GitHub pull request URL (required)"`
}

func registerOpenPR(s *mcp.Server, st *State) {
    mcp.AddTool(s, &mcp.Tool{
        Name: "open_pr",
        Description: "Atomically transition the worker's task to `pr_opened`, set " +
            "github_pr_url, and release the claim. After this call the worker is idle " +
            "and finalization (complete or fail) is performed by an admin via the " +
            "mastermind-mcp server, the elpulpo CLI, or the admin UI.",
    }, func(ctx context.Context, _ *mcp.CallToolRequest, in openPRInput) (*mcp.CallToolResult, struct{}, error) {
        if in.GithubPRURL == "" {
            return &mcp.CallToolResult{
                IsError: true,
                Content: []mcp.Content{&mcp.TextContent{Text: "open_pr: github_pr_url is required"}},
            }, struct{}{}, nil
        }
        if err := st.OpenPR(ctx, in.TaskID, in.GithubPRURL); err != nil {
            return toolErr(err, "open_pr"), struct{}{}, nil
        }
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: "PR opened; task parked. Worker is now idle."}},
        }, struct{}{}, nil
    })
}
```

- [ ] **Step 5: Run; expect PASS**

```bash
go test ./internal/worker/mcpserver -count=1 -v
```

Expected: PASS for the new tests and the entire package.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/mcpserver/
git commit -m "feat(worker-mcp): add set_jira_url and open_pr tools, surface instructions in TaskView"
```

---

## Stage 6 — Mastermind MCP and elpulpo CLI

### Task 6.1: `mastermind-mcp` tools — `request_review`, `finalize_task`

**Files:**
- Modify: `internal/mcpserver/tools.go`
- Modify: `internal/mcpserver/server.go` (only if it lists registrations explicitly)
- Modify: `internal/mcpserver/tools_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/tools_test.go`:

```go
func TestRequestReview_Tool(t *testing.T) {
    // Use the existing in-memory bufconn admin server fixture.
    fixture := newTestFixture(t)
    defer fixture.Close()

    created, _ := fixture.admin.CreateTask(context.Background(), &pb.CreateTaskRequest{
        Name: "t", Payload: []byte(`{"instructions":"go"}`),
    })
    // Drive task to pr_opened via the worker server (also exposed by fixture).
    _, _ = fixture.worker.ClaimTask(context.Background(), &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = fixture.worker.Heartbeat(context.Background(), &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = fixture.worker.OpenPR(context.Background(), &pb.OpenPRRequest{WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1"})

    res, err := callTool(t, fixture.server, "request_review", map[string]any{"id": created.GetTask().GetId()})
    if err != nil || res.IsError {
        t.Fatalf("request_review: err=%v isErr=%v body=%s", err, res.IsError, textOf(res))
    }
}

func TestFinalizeTask_Tool_Success(t *testing.T) {
    fixture := newTestFixture(t)
    defer fixture.Close()

    created, _ := fixture.admin.CreateTask(context.Background(), &pb.CreateTaskRequest{
        Name: "t", Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = fixture.worker.ClaimTask(context.Background(), &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = fixture.worker.Heartbeat(context.Background(), &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = fixture.worker.OpenPR(context.Background(), &pb.OpenPRRequest{WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1"})

    res, err := callTool(t, fixture.server, "finalize_task", map[string]any{
        "id":      created.GetTask().GetId(),
        "outcome": "success",
    })
    if err != nil || res.IsError {
        t.Fatalf("finalize_task: err=%v isErr=%v body=%s", err, res.IsError, textOf(res))
    }
}

func TestFinalizeTask_Tool_Failure(t *testing.T) {
    fixture := newTestFixture(t)
    defer fixture.Close()

    created, _ := fixture.admin.CreateTask(context.Background(), &pb.CreateTaskRequest{
        Name: "t", Payload: []byte(`{"instructions":"go"}`),
    })
    _, _ = fixture.worker.ClaimTask(context.Background(), &pb.ClaimTaskRequest{WorkerId: "w1"})
    _, _ = fixture.worker.Heartbeat(context.Background(), &pb.HeartbeatRequest{WorkerId: "w1", TaskId: created.GetTask().GetId()})
    _, _ = fixture.worker.OpenPR(context.Background(), &pb.OpenPRRequest{WorkerId: "w1", TaskId: created.GetTask().GetId(), GithubPrUrl: "https://github.com/o/r/pull/1"})

    res, err := callTool(t, fixture.server, "finalize_task", map[string]any{
        "id":      created.GetTask().GetId(),
        "outcome": "failure",
        "message": "rejected",
    })
    if err != nil || res.IsError {
        t.Fatalf("finalize_task: err=%v isErr=%v body=%s", err, res.IsError, textOf(res))
    }
}

func TestFinalizeTask_Tool_RejectsBadOutcome(t *testing.T) {
    fixture := newTestFixture(t)
    defer fixture.Close()

    res, _ := callTool(t, fixture.server, "finalize_task", map[string]any{
        "id":      uuid.New().String(),
        "outcome": "garbage",
    })
    if !res.IsError {
        t.Error("expected tool error for bad outcome")
    }
}
```

(Adapt `newTestFixture` and `callTool` to whatever the existing test file uses — most repos using the same scaffolding expose `fixture.admin` (an admin client) and `fixture.server` (an *mcp.Server* registered with admin tools). If the fixture exposes only the admin client and not a worker client, the tests can use the store directly to drive a task to `pr_opened` instead of going through the worker gRPC.)

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/mcpserver -run 'TestRequestReview_Tool|TestFinalizeTask_Tool' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Add the tools**

In `internal/mcpserver/tools.go`, where `Server` is constructed and `registerCreateTask`/`registerGetTask`/`registerListTasks` are called, add:

```go
registerRequestReview(s, admin)
registerFinalizeTask(s, admin)
```

Implement both:

```go
type RequestReviewInput struct {
    ID string `json:"id" jsonschema:"task id (UUID)"`
}

func registerRequestReview(s *mcp.Server, admin pb.AdminServiceClient) {
    mcp.AddTool(s, &mcp.Tool{
        Name:        "request_review",
        Description: "Move a parked `pr_opened` task to `review_requested`. Informational only.",
    }, func(ctx context.Context, _ *mcp.CallToolRequest, in RequestReviewInput) (*mcp.CallToolResult, TaskDetail, error) {
        resp, err := admin.RequestReview(ctx, &pb.RequestReviewRequest{Id: in.ID})
        if err != nil {
            return toolErr(err, "request_review"), TaskDetail{}, nil
        }
        d := fromProtoTask(resp.GetTask())
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s — %s", d.ID, d.Status)}},
        }, d, nil
    })
}

type FinalizeTaskInput struct {
    ID      string `json:"id" jsonschema:"task id (UUID)"`
    Outcome string `json:"outcome" jsonschema:"\"success\" or \"failure\" (required)"`
    Message string `json:"message,omitempty" jsonschema:"failure reason; ignored on success"`
}

func registerFinalizeTask(s *mcp.Server, admin pb.AdminServiceClient) {
    mcp.AddTool(s, &mcp.Tool{
        Name:        "finalize_task",
        Description: "Finalize a parked task with success or failure. Only allowed from `pr_opened` or `review_requested`. Always terminal — no retry.",
    }, func(ctx context.Context, _ *mcp.CallToolRequest, in FinalizeTaskInput) (*mcp.CallToolResult, TaskDetail, error) {
        req := &pb.FinalizeTaskRequest{Id: in.ID}
        switch in.Outcome {
        case "success":
            req.Outcome = &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}}
        case "failure":
            req.Outcome = &pb.FinalizeTaskRequest_Failure_{Failure: &pb.FinalizeTaskRequest_Failure{Message: in.Message}}
        default:
            return &mcp.CallToolResult{
                IsError: true,
                Content: []mcp.Content{&mcp.TextContent{Text: `finalize_task: outcome must be "success" or "failure"`}},
            }, TaskDetail{}, nil
        }
        resp, err := admin.FinalizeTask(ctx, req)
        if err != nil {
            return toolErr(err, "finalize_task"), TaskDetail{}, nil
        }
        d := fromProtoTask(resp.GetTask())
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s — %s", d.ID, d.Status)}},
        }, d, nil
    })
}
```

- [ ] **Step 4: Run; expect PASS**

```bash
go test ./internal/mcpserver -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/
git commit -m "feat(mastermind-mcp): add request_review and finalize_task tools"
```

### Task 6.2: `elpulpo` CLI — `tasks request-review`, `tasks finalize`, `--instructions` on `tasks create`

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/tasks.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:

```go
func TestTasksRequestReview(t *testing.T) {
    f := newCLIFixture(t) // existing helper
    defer f.Close()

    id := f.createParkedTask(t) // helper added below if not present

    err := cli.Run(context.Background(), []string{"tasks", "request-review", id}, &f.stdout, &f.stderr)
    if err != nil { t.Fatalf("Run: %v: %s", err, f.stderr.String()) }
    if !strings.Contains(f.stdout.String(), "review_requested") {
        t.Errorf("output: %q, want review_requested", f.stdout.String())
    }
}

func TestTasksFinalize_Success(t *testing.T) {
    f := newCLIFixture(t)
    defer f.Close()
    id := f.createParkedTask(t)

    if err := cli.Run(context.Background(), []string{"tasks", "finalize", id, "--success"}, &f.stdout, &f.stderr); err != nil {
        t.Fatalf("Run: %v: %s", err, f.stderr.String())
    }
    if !strings.Contains(f.stdout.String(), "completed") {
        t.Errorf("output: %q, want completed", f.stdout.String())
    }
}

func TestTasksFinalize_Failure(t *testing.T) {
    f := newCLIFixture(t)
    defer f.Close()
    id := f.createParkedTask(t)

    if err := cli.Run(context.Background(), []string{"tasks", "finalize", id, "--fail", "rejected"}, &f.stdout, &f.stderr); err != nil {
        t.Fatalf("Run: %v: %s", err, f.stderr.String())
    }
    if !strings.Contains(f.stdout.String(), "failed") {
        t.Errorf("output: %q, want failed", f.stdout.String())
    }
}

func TestTasksCreate_Instructions(t *testing.T) {
    f := newCLIFixture(t)
    defer f.Close()

    if err := cli.Run(context.Background(),
        []string{"tasks", "create", "--name", "t", "--instructions", "do the thing"},
        &f.stdout, &f.stderr); err != nil {
        t.Fatalf("Run: %v: %s", err, f.stderr.String())
    }
    // No InvalidArgument expected because the CLI now wraps --instructions
    // into a {"instructions":"..."} payload.
}
```

- [ ] **Step 2: Run; expect FAIL**

```bash
go test ./internal/cli -run 'TestTasksRequestReview|TestTasksFinalize|TestTasksCreate_Instructions' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Update the dispatcher in `tasks.go`**

In `runTasks`, add:

```go
case "request-review":
    return runTasksRequestReview(ctx, cfg, rest, stdout, stderr)
case "finalize":
    return runTasksFinalize(ctx, cfg, rest, stdout, stderr)
```

- [ ] **Step 4: Implement `runTasksRequestReview`**

Append to `internal/cli/tasks.go`:

```go
func runTasksRequestReview(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
    fs := flag.NewFlagSet("tasks request-review", flag.ContinueOnError)
    jsonOut := fs.Bool("json", false, "emit raw TaskDetail JSON instead of human summary")
    positional, err := parseFlags(fs, stderr, args)
    if err != nil {
        return err
    }
    if len(positional) != 1 {
        return errors.New("usage: elpulpo tasks request-review <id>")
    }
    client, closer, err := newAdminClient(ctx, cfg)
    if err != nil { return err }
    defer closer.Close()

    ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
    defer cancel()

    resp, err := client.RequestReview(ctx, &pb.RequestReviewRequest{Id: positional[0]})
    if err != nil { return formatErr(err) }
    return emitTask(stdout, resp.GetTask(), *jsonOut, fmt.Sprintf("requested review for %s (%s)", resp.GetTask().GetId(), resp.GetTask().GetStatus()))
}
```

- [ ] **Step 5: Implement `runTasksFinalize`**

```go
func runTasksFinalize(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
    fs := flag.NewFlagSet("tasks finalize", flag.ContinueOnError)
    var (
        succeed = fs.Bool("success", false, "finalize as completed")
        failMsg = fs.String("fail", "", "finalize as failed with this message")
        jsonOut = fs.Bool("json", false, "emit raw TaskDetail JSON instead of human summary")
    )
    positional, err := parseFlags(fs, stderr, args)
    if err != nil { return err }
    if len(positional) != 1 {
        return errors.New("usage: elpulpo tasks finalize <id> --success | --fail \"reason\"")
    }
    if *succeed == (*failMsg != "") {
        return errors.New("specify exactly one of --success or --fail \"reason\"")
    }
    req := &pb.FinalizeTaskRequest{Id: positional[0]}
    if *succeed {
        req.Outcome = &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}}
    } else {
        req.Outcome = &pb.FinalizeTaskRequest_Failure_{Failure: &pb.FinalizeTaskRequest_Failure{Message: *failMsg}}
    }
    client, closer, err := newAdminClient(ctx, cfg)
    if err != nil { return err }
    defer closer.Close()

    ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
    defer cancel()

    resp, err := client.FinalizeTask(ctx, req)
    if err != nil { return formatErr(err) }
    return emitTask(stdout, resp.GetTask(), *jsonOut, fmt.Sprintf("finalized %s (%s)", resp.GetTask().GetId(), resp.GetTask().GetStatus()))
}
```

- [ ] **Step 6: Add `--instructions` flag to `runTasksCreate`**

In `runTasksCreate`, declare:

```go
instructions = fs.String("instructions", "", "instructions text; @path reads from file, - reads stdin")
```

and after `req` is built but before the existing `payload` block, add:

```go
if *instructions != "" {
    raw, err := readPayload(*instructions, stderr)
    if err != nil {
        return err
    }
    // Merge into payload as {"instructions": "..."} unless the caller also
    // supplied --payload, in which case we splice the field into the user's
    // JSON.
    if len(req.Payload) == 0 {
        b, _ := json.Marshal(map[string]string{"instructions": string(raw)})
        req.Payload = b
    } else {
        var m map[string]any
        if err := json.Unmarshal(req.Payload, &m); err != nil || m == nil {
            return errors.New("--payload must be a JSON object when --instructions is also set")
        }
        m["instructions"] = string(raw)
        b, _ := json.Marshal(m)
        req.Payload = b
    }
}
```

Update the usage string in `cli.go`'s `printUsage` to include the new commands and flag:

```text
  elpulpo tasks create   --name NAME [--instructions TEXT|@file|-]
                         [--payload JSON] [--priority N]
                         [--max-attempts N] [--scheduled-for RFC3339]
                         [--jira-url URL] [--github-pr-url URL]
  elpulpo tasks request-review <id>
  elpulpo tasks finalize       <id> --success | --fail "reason"
```

- [ ] **Step 7: Update the `--status` filter help text** in `runTasksList` (`tasks.go`) to include the new states:

```go
statusF = fs.String("status", "", "filter: pending|claimed|in_progress|pr_opened|review_requested|completed|failed")
```

- [ ] **Step 8: Run; expect PASS**

```bash
go test ./internal/cli -count=1 -v
```

Expected: PASS for new tests; existing tests still green.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/
git commit -m "feat(elpulpo): add tasks request-review/finalize, --instructions flag, status filter help"
```

---

## Stage 7 — Admin UI (HTTP)

### Task 7.1: Routes and template updates

**Files:**
- Modify: `internal/mastermind/httpserver/handlers.go`
- Modify: `internal/mastermind/httpserver/server.go` (route registration)
- Modify: `internal/mastermind/httpserver/templates/tasks_detail.html`
- Modify: `internal/mastermind/httpserver/handlers_test.go`

- [ ] **Step 1: Add the route registrations**

In `internal/mastermind/httpserver/server.go` (or wherever routes are wired), register:

```
POST /tasks/{id}/request-review
POST /tasks/{id}/finalize          (form fields: outcome=success|failure, message=...)
```

Using the existing pattern in the repo (likely `mux.HandleFunc` or a router method). Each handler delegates to a new method on the http handlers struct.

- [ ] **Step 2: Implement the handlers**

Append to `internal/mastermind/httpserver/handlers.go`:

```go
func (h *Handlers) PostRequestReview(w http.ResponseWriter, r *http.Request) {
    id, err := uuid.Parse(r.PathValue("id"))
    if err != nil {
        http.Error(w, "bad id", http.StatusBadRequest)
        return
    }
    err = h.store.RequestReview(r.Context(), id)
    switch {
    case err == nil:
        h.redirectToTask(w, r, id)
    case errors.Is(err, store.ErrNotFound):
        http.NotFound(w, r)
    case errors.Is(err, store.ErrInvalidTransition):
        http.Error(w, "task is not in pr_opened", http.StatusConflict)
    default:
        http.Error(w, err.Error(), http.StatusInternalServerError)
    }
}

func (h *Handlers) PostFinalize(w http.ResponseWriter, r *http.Request) {
    id, err := uuid.Parse(r.PathValue("id"))
    if err != nil { http.Error(w, "bad id", http.StatusBadRequest); return }
    if err := r.ParseForm(); err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }

    var success bool
    switch r.PostFormValue("outcome") {
    case "success":
        success = true
    case "failure":
        success = false
    default:
        http.Error(w, `outcome must be "success" or "failure"`, http.StatusBadRequest)
        return
    }
    err = h.store.FinalizeTask(r.Context(), id, success, r.PostFormValue("message"))
    switch {
    case err == nil:
        h.redirectToTask(w, r, id)
    case errors.Is(err, store.ErrNotFound):
        http.NotFound(w, r)
    case errors.Is(err, store.ErrInvalidTransition):
        http.Error(w, "task is not in pr_opened or review_requested", http.StatusConflict)
    default:
        http.Error(w, err.Error(), http.StatusInternalServerError)
    }
}
```

`h.redirectToTask` is the existing pattern used by other admin POST handlers in this file — follow whatever the existing code does (`http.Redirect(w, r, "/tasks/"+id.String(), http.StatusSeeOther)` or similar).

- [ ] **Step 3: Update `tasks_detail.html`** to render `instructions` near the top and parked-state action forms

Add a section near the top of the task body (above the existing payload display):

```html
{{ if .Instructions }}
<section class="task-instructions">
  <h3>Instructions</h3>
  <pre class="instructions">{{ .Instructions }}</pre>
</section>
{{ end }}
```

Add parked-state action forms in a sensible location:

```html
{{ if eq (printf "%s" .Task.Status) "pr_opened" }}
<form method="post" action="/tasks/{{ .Task.Id }}/request-review" class="inline-form">
  <button type="submit">Mark reviewed</button>
</form>
{{ end }}

{{ if or (eq (printf "%s" .Task.Status) "pr_opened") (eq (printf "%s" .Task.Status) "review_requested") }}
<form method="post" action="/tasks/{{ .Task.Id }}/finalize" class="inline-form">
  <label>Outcome:
    <select name="outcome">
      <option value="success">success</option>
      <option value="failure">failure</option>
    </select>
  </label>
  <label>Message: <input type="text" name="message" placeholder="(failure reason, optional)"></label>
  <button type="submit">Finalize</button>
</form>
{{ end }}
```

In the handler that renders the template, populate `.Instructions` by parsing `payload.instructions`:

```go
type TaskDetailView struct {
    Task         store.Task
    Instructions string
    // ...existing fields...
}

func instructionsFrom(payload []byte) string {
    var v struct{ Instructions string `json:"instructions"` }
    _ = json.Unmarshal(payload, &v)
    return v.Instructions
}
```

- [ ] **Step 4: Run unit tests**

```bash
go test ./internal/mastermind/httpserver -count=1 -v
```

Expected: PASS. If a fixture needs updating to drive a task to `pr_opened` for the new buttons, follow Stage 4's gRPC test pattern.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/httpserver/
git commit -m "feat(admin-ui): render instructions and parked-task action buttons"
```

---

## Stage 8 — End-to-end happy path

### Task 8.1: e2e PR pipeline test

**Files:**
- Modify: `internal/e2e/e2e_test.go`

- [ ] **Step 1: Add a happy-path scenario**

Append to `internal/e2e/e2e_test.go` (adapt to whatever fixture types and helpers already exist):

```go
func TestE2E_PRPipelineHappyPath(t *testing.T) {
    f := newE2EFixture(t) // existing helper that brings up store + grpc server
    defer f.Close()

    ctx := context.Background()
    created, err := f.admin.CreateTask(ctx, &pb.CreateTaskRequest{
        Name:    "feature",
        Payload: []byte(`{"instructions":"implement X"}`),
    })
    if err != nil { t.Fatalf("CreateTask: %v", err) }
    id := created.GetTask().GetId()

    // Worker side.
    claim, err := f.worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
    if err != nil { t.Fatalf("ClaimTask: %v", err) }
    if claim.GetTask().GetId() != id { t.Fatalf("claim id mismatch") }
    if _, err := f.worker.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: id}); err != nil {
        t.Fatalf("Heartbeat: %v", err)
    }
    if _, err := f.worker.SetJiraURL(ctx, &pb.SetJiraURLRequest{WorkerId: "w1", TaskId: id, Url: "https://jira/T-1"}); err != nil {
        t.Fatalf("SetJiraURL: %v", err)
    }
    if _, err := f.worker.OpenPR(ctx, &pb.OpenPRRequest{WorkerId: "w1", TaskId: id, GithubPrUrl: "https://github.com/o/r/pull/1"}); err != nil {
        t.Fatalf("OpenPR: %v", err)
    }

    // After OpenPR the worker is freed; ClaimTask should now return NOT_FOUND.
    if _, err := f.worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"}); status.Code(err) != codes.NotFound {
        t.Errorf("after OpenPR, ClaimTask got %v, want NotFound (queue empty)", err)
    }

    // Admin-side finalization.
    if _, err := f.admin.RequestReview(ctx, &pb.RequestReviewRequest{Id: id}); err != nil {
        t.Fatalf("RequestReview: %v", err)
    }
    if _, err := f.admin.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
        Id: id, Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
    }); err != nil {
        t.Fatalf("FinalizeTask: %v", err)
    }

    got, _ := f.admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
    if got.GetTask().GetStatus() != "completed" {
        t.Errorf("final status=%q, want completed", got.GetTask().GetStatus())
    }
    if got.GetTask().GetJiraUrl() != "https://jira/T-1" {
        t.Errorf("jira_url=%q, want preserved", got.GetTask().GetJiraUrl())
    }
    if got.GetTask().GetGithubPrUrl() != "https://github.com/o/r/pull/1" {
        t.Errorf("github_pr_url=%q, want preserved", got.GetTask().GetGithubPrUrl())
    }
}
```

- [ ] **Step 2: Run; expect PASS**

```bash
go test ./internal/e2e -run TestE2E_PRPipelineHappyPath -count=1 -v
```

Expected: PASS.

- [ ] **Step 3: Run the full suite to verify nothing else regressed**

```bash
go test ./... -race -count=1
```

Expected: PASS for every package.

- [ ] **Step 4: Commit**

```bash
git add internal/e2e/
git commit -m "test(e2e): happy-path PR pipeline (claim -> open_pr -> request_review -> finalize)"
```

---

## Stage 9 — Documentation

### Task 9.1: Update `README.md`

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add the new MCP tools to the worker MCP table**

Insert two rows in the worker MCP tool table:

```
| `set_jira_url`    | Attach a JIRA URL to the claimed task (allowed any time during the claim). |
| `open_pr`         | Atomically transition to `pr_opened`, set github_pr_url, and release the claim. After this call the worker is idle; finalization is admin-only. |
```

Add a short paragraph after the table:

> Once a worker calls `open_pr`, the task is "parked" — it leaves `in_progress`, the claim is released, and only an admin (via `mastermind-mcp`, `elpulpo`, or the admin UI) can finalize it with `complete`/`fail` or move it to `review_requested`. The worker is freed to claim another task immediately.

Update the `elpulpo` CLI commands table to add:

```
| `elpulpo tasks request-review <id>`             | Move a `pr_opened` task to `review_requested`. |
| `elpulpo tasks finalize <id> --success`         | Terminal admin completion of a parked task.    |
| `elpulpo tasks finalize <id> --fail "reason"`   | Terminal admin failure of a parked task.       |
```

Add `--instructions TEXT|@file|-` to the `tasks create` row and document that `payload.instructions` is now required at create time.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: README — payload.instructions, set_jira_url/open_pr, request-review/finalize"
```

---

## Self-review checklist

After every task is checked off, verify:

- [ ] `go test ./... -race -count=1` passes
- [ ] `go build ./...` is clean
- [ ] `git log --oneline` shows commits in stage order with `feat(...)`/`refactor(...)`/`test(...)`/`docs:` prefixes per the existing repo convention
- [ ] `internal/proto/tasks.proto` and the regenerated `.pb.go` files are in sync (run `make proto` and check there's no diff)
- [ ] `migrations/000005_extend_task_states.{up,down}.sql` apply cleanly against an empty database
- [ ] Spec file `docs/superpowers/specs/2026-04-27-worker-real-tasks-design.md` matches the implementation — no behavioral drift was introduced silently

If any of these fail, do not open a PR; loop back to the relevant stage.
