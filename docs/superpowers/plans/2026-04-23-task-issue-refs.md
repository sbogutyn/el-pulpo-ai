# Task issue refs (JIRA + GitHub PR) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two optional per-task reference fields — `jira_url` and `github_pr_url` — stored as full URLs, validated strictly on save, rendered in the admin UI as short-form badges (`PROJ-123`, `org/repo#3213`) that open the full URL in a new tab, and editable in any task status (not just `pending`).

**Architecture:** A new Postgres migration adds two nullable TEXT columns. A small Go package `internal/mastermind/issuerefs` owns validation regexes and short-form extraction; it is used both by the HTTP form-parsing path and by the admin templates (registered as template funcs). The `store` package grows a dedicated `UpdateTaskLinks` method that bypasses the pending-only rule so refs can be attached to running/completed/failed tasks. The worker-facing gRPC `Task` proto is unchanged.

**Tech Stack:** Go 1.25, `pgx/v5`, `html/template`, `html/template`-registered funcs, `golang-migrate` (via testcontainers in tests), Pico CSS + HTMX for the admin UI.

**Spec:** `docs/superpowers/specs/2026-04-23-task-issue-refs-design.md`

---

## File structure

Files to create:
- `migrations/000002_add_task_issue_refs.up.sql`
- `migrations/000002_add_task_issue_refs.down.sql`
- `internal/mastermind/issuerefs/issuerefs.go`
- `internal/mastermind/issuerefs/issuerefs_test.go`

Files to modify:
- `internal/mastermind/store/tasks.go` — extend `Task`, `NewTaskInput`, `UpdateTaskInput`, `taskColumns`, `scanTask`, `CreateTask`, `UpdateTask`; add `UpdateTaskLinks`.
- `internal/mastermind/store/tasks_test.go` — round-trip test for the new fields.
- `internal/mastermind/store/tasks_update_test.go` — tests for `UpdateTaskLinks` and requeue preservation.
- `internal/mastermind/httpserver/server.go` — register `jiraShort`/`prShort` template funcs during parse.
- `internal/mastermind/httpserver/handlers.go` — extend `taskForm`, `parseTaskForm`, `formFromTask`; add `tasksUpdateLinks` and its route verb.
- `internal/mastermind/httpserver/handlers_test.go` — validation and round-trip tests.
- `internal/mastermind/httpserver/templates/tasks_new.html` — two optional inputs.
- `internal/mastermind/httpserver/templates/tasks_edit.html` — two optional inputs.
- `internal/mastermind/httpserver/templates/tasks_fragment.html` — `Refs` column + update `colspan`.
- `internal/mastermind/httpserver/templates/tasks_detail.html` — render links + inline update form.

Files NOT touched:
- `internal/proto/*` — gRPC surface stays admin-free.
- `internal/worker/*` — workers never see these fields.

---

## Task 1: Migration 000002 — add columns

**Files:**
- Create: `migrations/000002_add_task_issue_refs.up.sql`
- Create: `migrations/000002_add_task_issue_refs.down.sql`

Migrations run automatically in the store/httpserver test setups. This task is a prerequisite for every later test in this plan, so we commit it before writing any test that needs the columns.

- [ ] **Step 1: Create up migration**

File: `migrations/000002_add_task_issue_refs.up.sql`

```sql
ALTER TABLE tasks
  ADD COLUMN jira_url      TEXT,
  ADD COLUMN github_pr_url TEXT;
```

- [ ] **Step 2: Create down migration**

File: `migrations/000002_add_task_issue_refs.down.sql`

```sql
ALTER TABLE tasks
  DROP COLUMN github_pr_url,
  DROP COLUMN jira_url;
```

- [ ] **Step 3: Sanity check — existing store tests still pass**

Run: `go test ./internal/mastermind/store/...`

Expected: PASS. The migration applies cleanly (testcontainers bring up a fresh DB per `TestMain`); existing queries don't reference the new columns so they keep working.

- [ ] **Step 4: Commit**

```bash
git add migrations/000002_add_task_issue_refs.up.sql migrations/000002_add_task_issue_refs.down.sql
git commit -m "feat(migrations): add jira_url and github_pr_url to tasks"
```

---

## Task 2: `issuerefs` package — JIRA validation + short form (TDD)

**Files:**
- Create: `internal/mastermind/issuerefs/issuerefs.go`
- Create: `internal/mastermind/issuerefs/issuerefs_test.go`

Build the package incrementally, one field at a time. Start with JIRA.

- [ ] **Step 1: Write the failing test**

File: `internal/mastermind/issuerefs/issuerefs_test.go`

```go
package issuerefs

import (
	"errors"
	"testing"
)

func TestValidateJira(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"cloud canonical", "https://acme.atlassian.net/browse/PROJ-123", nil},
		{"self-hosted", "http://jira.internal.example/browse/AB-1", nil},
		{"with query", "https://acme.atlassian.net/browse/PROJ-123?focusedCommentId=1", nil},
		{"with fragment", "https://acme.atlassian.net/browse/PROJ-123#comments", nil},
		{"missing scheme", "acme.atlassian.net/browse/PROJ-123", ErrInvalidJira},
		{"wrong path", "https://acme.atlassian.net/issues/PROJ-123", ErrInvalidJira},
		{"lowercase key", "https://acme.atlassian.net/browse/proj-123", ErrInvalidJira},
		{"no number", "https://acme.atlassian.net/browse/PROJ", ErrInvalidJira},
		{"empty", "", ErrInvalidJira},
		{"trailing slash", "https://acme.atlassian.net/browse/PROJ-123/", ErrInvalidJira},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateJira(tc.in)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("ValidateJira(%q): got %v, want %v", tc.in, got, tc.wantErr)
			}
		})
	}
}

func TestJiraShort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://acme.atlassian.net/browse/PROJ-123", "PROJ-123"},
		{"https://acme.atlassian.net/browse/AB_C-9?x=1", "AB_C-9"},
		{"https://acme.atlassian.net/browse/PROJ-123#comments", "PROJ-123"},
		{"", ""},
		{"not a url", "not a url"}, // fallback
	}
	for _, tc := range cases {
		if got := JiraShort(tc.in); got != tc.want {
			t.Errorf("JiraShort(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mastermind/issuerefs/...`

Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Write minimal implementation**

File: `internal/mastermind/issuerefs/issuerefs.go`

```go
// Package issuerefs validates and shortens JIRA and GitHub PR URLs used as
// per-task reference metadata. The validation is strict — URLs that don't
// match the expected shapes are rejected at the admin write path — so the
// Short functions only have to handle the canonical forms. They also degrade
// gracefully on surprise inputs by returning the raw URL.
package issuerefs

import (
	"errors"
	"regexp"
)

var (
	ErrInvalidJira = errors.New("invalid JIRA URL")

	jiraRE = regexp.MustCompile(`^https?://[^/]+/browse/([A-Z][A-Z0-9_]+-\d+)(?:[?#].*)?$`)
)

func ValidateJira(s string) error {
	if !jiraRE.MatchString(s) {
		return ErrInvalidJira
	}
	return nil
}

func JiraShort(url string) string {
	if url == "" {
		return ""
	}
	m := jiraRE.FindStringSubmatch(url)
	if len(m) < 2 {
		return url
	}
	return m[1]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mastermind/issuerefs/...`

Expected: PASS for `TestValidateJira` and `TestJiraShort`.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/issuerefs/
git commit -m "feat(issuerefs): validate and shorten JIRA URLs"
```

---

## Task 3: `issuerefs` package — GitHub PR validation + short form (TDD)

**Files:**
- Modify: `internal/mastermind/issuerefs/issuerefs_test.go`
- Modify: `internal/mastermind/issuerefs/issuerefs.go`

- [ ] **Step 1: Add failing tests for PR**

Append to `internal/mastermind/issuerefs/issuerefs_test.go`:

```go
func TestValidatePR(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"github.com", "https://github.com/acme/widget/pull/3213", nil},
		{"enterprise", "https://ghe.internal.example/acme/widget/pull/3213", nil},
		{"with query", "https://github.com/acme/widget/pull/3213?files=1", nil},
		{"with fragment", "https://github.com/acme/widget/pull/3213#discussion_r1", nil},
		{"missing scheme", "github.com/acme/widget/pull/3213", ErrInvalidPR},
		{"wrong verb", "https://github.com/acme/widget/issues/3213", ErrInvalidPR},
		{"no repo", "https://github.com/acme/pull/3213", ErrInvalidPR},
		{"non-numeric id", "https://github.com/acme/widget/pull/abc", ErrInvalidPR},
		{"trailing path", "https://github.com/acme/widget/pull/3213/files", ErrInvalidPR},
		{"empty", "", ErrInvalidPR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidatePR(tc.in)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("ValidatePR(%q): got %v, want %v", tc.in, got, tc.wantErr)
			}
		})
	}
}

func TestPRShort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/acme/widget/pull/3213", "acme/widget#3213"},
		{"https://ghe.internal.example/acme/widget/pull/7", "acme/widget#7"},
		{"https://github.com/acme/widget/pull/3213?files=1", "acme/widget#3213"},
		{"", ""},
		{"not a url", "not a url"}, // fallback
	}
	for _, tc := range cases {
		if got := PRShort(tc.in); got != tc.want {
			t.Errorf("PRShort(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mastermind/issuerefs/...`

Expected: FAIL — `ValidatePR`, `PRShort`, `ErrInvalidPR` undefined.

- [ ] **Step 3: Add PR implementation**

Edit `internal/mastermind/issuerefs/issuerefs.go`. Extend the `var` block and add two new functions:

```go
var (
	ErrInvalidJira = errors.New("invalid JIRA URL")
	ErrInvalidPR   = errors.New("invalid GitHub PR URL")

	jiraRE = regexp.MustCompile(`^https?://[^/]+/browse/([A-Z][A-Z0-9_]+-\d+)(?:[?#].*)?$`)
	prRE   = regexp.MustCompile(`^https?://[^/]+/([^/]+)/([^/]+)/pull/(\d+)(?:[?#].*)?$`)
)

func ValidatePR(s string) error {
	if !prRE.MatchString(s) {
		return ErrInvalidPR
	}
	return nil
}

func PRShort(url string) string {
	if url == "" {
		return ""
	}
	m := prRE.FindStringSubmatch(url)
	if len(m) < 4 {
		return url
	}
	return m[1] + "/" + m[2] + "#" + m[3]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mastermind/issuerefs/...`

Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/issuerefs/
git commit -m "feat(issuerefs): validate and shorten GitHub PR URLs"
```

---

## Task 4: Store — extend `Task`, inputs, and read/write paths (TDD)

**Files:**
- Modify: `internal/mastermind/store/tasks.go`
- Modify: `internal/mastermind/store/tasks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/store/tasks_test.go`:

```go
func TestCreateAndGetTask_WithIssueRefs(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	jira := "https://acme.atlassian.net/browse/PROJ-1"
	pr := "https://github.com/acme/widget/pull/7"

	created, err := s.CreateTask(ctx, NewTaskInput{
		Name:        "with-refs",
		Payload:     json.RawMessage(`{}`),
		MaxAttempts: 3,
		JiraURL:     &jira,
		GithubPRURL: &pr,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.JiraURL == nil || *created.JiraURL != jira {
		t.Errorf("JiraURL: got %v, want %q", created.JiraURL, jira)
	}
	if created.GithubPRURL == nil || *created.GithubPRURL != pr {
		t.Errorf("GithubPRURL: got %v, want %q", created.GithubPRURL, pr)
	}

	got, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.JiraURL == nil || *got.JiraURL != jira {
		t.Errorf("GetTask JiraURL mismatch: %v", got.JiraURL)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != pr {
		t.Errorf("GetTask GithubPRURL mismatch: %v", got.GithubPRURL)
	}
}

func TestCreateTask_NoIssueRefs_StoresNull(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, err := s.CreateTask(ctx, NewTaskInput{
		Name: "no-refs", Payload: json.RawMessage(`{}`), MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.JiraURL != nil || created.GithubPRURL != nil {
		t.Errorf("expected nil refs, got jira=%v pr=%v", created.JiraURL, created.GithubPRURL)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mastermind/store/... -run Issue`

Expected: FAIL — `JiraURL`/`GithubPRURL` fields don't exist on `Task` or `NewTaskInput`.

- [ ] **Step 3: Extend struct + inputs + read/write paths**

In `internal/mastermind/store/tasks.go`:

1. Extend `Task`:

```go
type Task struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Payload         json.RawMessage `json:"payload"`
	Priority        int             `json:"priority"`
	Status          TaskStatus      `json:"status"`
	ScheduledFor    *time.Time      `json:"scheduled_for,omitempty"`
	AttemptCount    int             `json:"attempt_count"`
	MaxAttempts     int             `json:"max_attempts"`
	ClaimedBy       *string         `json:"claimed_by,omitempty"`
	ClaimedAt       *time.Time      `json:"claimed_at,omitempty"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	LastError       *string         `json:"last_error,omitempty"`
	JiraURL         *string         `json:"jira_url,omitempty"`
	GithubPRURL     *string         `json:"github_pr_url,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}
```

2. Extend `NewTaskInput`:

```go
type NewTaskInput struct {
	Name         string
	Payload      json.RawMessage
	Priority     int
	MaxAttempts  int
	ScheduledFor *time.Time
	JiraURL      *string
	GithubPRURL  *string
}
```

3. Extend `UpdateTaskInput`:

```go
type UpdateTaskInput struct {
	Name         string
	Priority     int
	MaxAttempts  int
	ScheduledFor *time.Time
	Payload      json.RawMessage
	JiraURL      *string
	GithubPRURL  *string
}
```

4. Extend `taskColumns`:

```go
const taskColumns = `
  id, name, payload, priority, status, scheduled_for,
  attempt_count, max_attempts,
  claimed_by, claimed_at, last_heartbeat_at,
  completed_at, last_error,
  jira_url, github_pr_url,
  created_at, updated_at
`
```

5. Extend `scanTask` to match the new column order:

```go
func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(
		&t.ID, &t.Name, &t.Payload, &t.Priority, &t.Status, &t.ScheduledFor,
		&t.AttemptCount, &t.MaxAttempts,
		&t.ClaimedBy, &t.ClaimedAt, &t.LastHeartbeatAt,
		&t.CompletedAt, &t.LastError,
		&t.JiraURL, &t.GithubPRURL,
		&t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}
```

6. Extend `CreateTask` INSERT to include both columns:

```go
func (s *Store) CreateTask(ctx context.Context, in NewTaskInput) (Task, error) {
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = 3
	}
	if in.Payload == nil {
		in.Payload = json.RawMessage(`{}`)
	}
	row := s.pool.QueryRow(ctx, `
      INSERT INTO tasks (name, payload, priority, max_attempts, scheduled_for, jira_url, github_pr_url)
      VALUES ($1, $2, $3, $4, $5, $6, $7)
      RETURNING `+taskColumns,
		in.Name, in.Payload, in.Priority, in.MaxAttempts, in.ScheduledFor, in.JiraURL, in.GithubPRURL,
	)
	return scanTask(row)
}
```

7. Extend `UpdateTask` to carry refs through the pending-only update:

```go
func (s *Store) UpdateTask(ctx context.Context, id uuid.UUID, in UpdateTaskInput) (Task, error) {
	row := s.pool.QueryRow(ctx, `
      UPDATE tasks
      SET name          = $2,
          priority      = $3,
          max_attempts  = $4,
          scheduled_for = $5,
          payload       = COALESCE($6, payload),
          jira_url      = $7,
          github_pr_url = $8,
          updated_at    = now()
      WHERE id = $1 AND status = 'pending'
      RETURNING `+taskColumns,
		id, in.Name, in.Priority, in.MaxAttempts, in.ScheduledFor, in.Payload, in.JiraURL, in.GithubPRURL,
	)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetTask(ctx, id); errors.Is(getErr, ErrNotFound) {
			return Task{}, ErrNotFound
		}
		return Task{}, ErrNotEditable
	}
	return t, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mastermind/store/...`

Expected: PASS — the new tests plus every existing store test.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/tasks.go internal/mastermind/store/tasks_test.go
git commit -m "feat(store): persist jira_url and github_pr_url on tasks"
```

---

## Task 5: Store — `UpdateTaskLinks` method (TDD)

**Files:**
- Modify: `internal/mastermind/store/tasks.go`
- Modify: `internal/mastermind/store/tasks_update_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/store/tasks_update_test.go`:

```go
func TestUpdateTaskLinks_WorksInAnyStatus(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "x", MaxAttempts: 3})

	// Force a status where UpdateTask would fail.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='failed' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}

	jira := "https://acme.atlassian.net/browse/PROJ-9"
	pr := "https://github.com/acme/widget/pull/42"
	upd, err := s.UpdateTaskLinks(ctx, created.ID, &jira, &pr)
	if err != nil {
		t.Fatalf("UpdateTaskLinks: %v", err)
	}
	if upd.JiraURL == nil || *upd.JiraURL != jira {
		t.Errorf("JiraURL mismatch: %v", upd.JiraURL)
	}
	if upd.GithubPRURL == nil || *upd.GithubPRURL != pr {
		t.Errorf("GithubPRURL mismatch: %v", upd.GithubPRURL)
	}
	if upd.Status != "failed" {
		t.Errorf("status changed: %q", upd.Status)
	}
}

func TestUpdateTaskLinks_NilClears(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	jira := "https://acme.atlassian.net/browse/PROJ-9"
	pr := "https://github.com/acme/widget/pull/42"
	created, _ := s.CreateTask(ctx, NewTaskInput{
		Name: "x", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})

	upd, err := s.UpdateTaskLinks(ctx, created.ID, nil, nil)
	if err != nil {
		t.Fatalf("UpdateTaskLinks: %v", err)
	}
	if upd.JiraURL != nil {
		t.Errorf("JiraURL not cleared: %v", *upd.JiraURL)
	}
	if upd.GithubPRURL != nil {
		t.Errorf("GithubPRURL not cleared: %v", *upd.GithubPRURL)
	}
}

func TestUpdateTaskLinks_NotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, err := s.UpdateTaskLinks(ctx, uuid.New(), nil, nil)
	if err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
```

The file currently imports `context`, `testing`, `time`. Add `"github.com/google/uuid"` to the import block (required for `TestUpdateTaskLinks_NotFound`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mastermind/store/... -run UpdateTaskLinks`

Expected: FAIL — `UpdateTaskLinks` undefined.

- [ ] **Step 3: Implement `UpdateTaskLinks`**

Append to `internal/mastermind/store/tasks.go` (near `UpdateTask`):

```go
// UpdateTaskLinks sets the JIRA and GitHub PR reference URLs for a task.
// Unlike UpdateTask, this works regardless of the task's current status:
// the refs are documentation, not execution state, and are routinely
// attached after the task has already run.
//
// Nil pointers persist as SQL NULL, clearing a previously-set link.
func (s *Store) UpdateTaskLinks(
	ctx context.Context, id uuid.UUID, jira, pr *string,
) (Task, error) {
	row := s.pool.QueryRow(ctx, `
      UPDATE tasks
      SET jira_url      = $2,
          github_pr_url = $3,
          updated_at    = now()
      WHERE id = $1
      RETURNING `+taskColumns, id, jira, pr)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mastermind/store/...`

Expected: PASS for all three new tests and every existing test.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/store/tasks.go internal/mastermind/store/tasks_update_test.go
git commit -m "feat(store): add UpdateTaskLinks for edit-in-any-status refs"
```

---

## Task 6: Store — assert `RequeueTask` preserves refs (TDD, defensive)

**Files:**
- Modify: `internal/mastermind/store/tasks_update_test.go`

`RequeueTask` currently doesn't touch `jira_url` or `github_pr_url`, which is the desired behavior. Lock it in with a test so a future edit doesn't silently wipe refs on requeue.

- [ ] **Step 1: Write the test**

Append to `internal/mastermind/store/tasks_update_test.go`:

```go
func TestRequeueTask_PreservesIssueRefs(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	jira := "https://acme.atlassian.net/browse/PROJ-77"
	pr := "https://github.com/acme/widget/pull/5"
	created, _ := s.CreateTask(ctx, NewTaskInput{
		Name: "x", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='failed' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}

	reset, err := s.RequeueTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("RequeueTask: %v", err)
	}
	if reset.JiraURL == nil || *reset.JiraURL != jira {
		t.Errorf("JiraURL wiped on requeue: %v", reset.JiraURL)
	}
	if reset.GithubPRURL == nil || *reset.GithubPRURL != pr {
		t.Errorf("GithubPRURL wiped on requeue: %v", reset.GithubPRURL)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/mastermind/store/... -run RequeueTask_PreservesIssueRefs`

Expected: PASS without any code change — `RequeueTask` only touches execution columns.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/store/tasks_update_test.go
git commit -m "test(store): assert requeue preserves issue refs"
```

---

## Task 7: Register `jiraShort` / `prShort` template funcs

**Files:**
- Modify: `internal/mastermind/httpserver/server.go`

Templates need to call `{{ .JiraURL | jiraShort }}` and `{{ .GithubPRURL | prShort }}`. `html/template`'s `ParseFS` doesn't attach funcs after the fact — funcs must be set on the template *before* any reference is parsed. So we change the builder to `template.New("").Funcs(...).ParseFS(...)`.

- [ ] **Step 1: Replace the template build loop**

In `internal/mastermind/httpserver/server.go`, replace the current `New` function's template-parsing block (lines ~40-58) with:

```go
func New(s *store.Store, cfg Config, log *slog.Logger) (*Server, error) {
	funcs := template.FuncMap{
		"jiraShort": issuerefs.JiraShort,
		"prShort":   issuerefs.PRShort,
	}

	pages := map[string]*template.Template{}
	pageFiles := map[string][]string{
		"tasks_list":   {"templates/base.html", "templates/tasks_list.html", "templates/tasks_fragment.html"},
		"tasks_new":    {"templates/base.html", "templates/tasks_new.html"},
		"tasks_edit":   {"templates/base.html", "templates/tasks_edit.html"},
		"tasks_detail": {"templates/base.html", "templates/tasks_detail.html"},
	}
	for name, files := range pageFiles {
		t, err := template.New("").Funcs(funcs).ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	fragTree, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/tasks_fragment.html")
	if err != nil {
		return nil, fmt.Errorf("parse tasks_fragment: %w", err)
	}
	pages["tasks_fragment"] = fragTree

	srv := &Server{store: s, cfg: cfg, log: log, pages: pages, mux: http.NewServeMux()}
	srv.routes()
	return srv, nil
}
```

Add the import at the top of the file:

```go
import (
	// ... existing imports ...
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/issuerefs"
)
```

- [ ] **Step 2: Run the existing httpserver tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS — templates still render because we only added funcs; we haven't used them in any template yet.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/httpserver/server.go
git commit -m "feat(httpserver): register jiraShort and prShort template funcs"
```

---

## Task 8: `taskForm` + `parseTaskForm` + `formFromTask` carry refs (TDD)

**Files:**
- Modify: `internal/mastermind/httpserver/handlers.go`
- Modify: `internal/mastermind/httpserver/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/httpserver/handlers_test.go`:

```go
func TestCreateTask_WithIssueRefs(t *testing.T) {
	srv := newServer(t)

	form := url.Values{
		"name":          {"with-refs"},
		"priority":      {"0"},
		"max_attempts":  {"3"},
		"payload":       {"{}"},
		"jira_url":      {"https://acme.atlassian.net/browse/PROJ-1"},
		"github_pr_url": {"https://github.com/acme/widget/pull/7"},
	}.Encode()

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PROJ-1") {
		t.Errorf("list missing JIRA short form: %s", body)
	}
	if !strings.Contains(body, "acme/widget#7") {
		t.Errorf("list missing PR short form: %s", body)
	}
}

func TestCreateTask_InvalidJiraURL(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"name":     {"x"},
		"payload":  {"{}"},
		"jira_url": {"not-a-jira-url"},
	}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "JIRA") {
		t.Errorf("response missing JIRA error hint: %s", rr.Body.String())
	}
}

func TestCreateTask_InvalidPRURL(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"name":          {"x"},
		"payload":       {"{}"},
		"github_pr_url": {"https://github.com/x/y/issues/1"},
	}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
}
```

Note: this test reads the list page HTML and expects the short forms to be present. That means the template updates in Task 10 and Task 12 must also land for this test to pass. We'll structure Task 8 to cover handlers + parsing only and keep this test temporarily skipped until the template work is done. Mark the whole-round-trip test `TestCreateTask_WithIssueRefs` with `t.Skip` for now — remove the skip in Task 12.

Change the first line of `TestCreateTask_WithIssueRefs` to:

```go
func TestCreateTask_WithIssueRefs(t *testing.T) {
	t.Skip("enabled in Task 12 once list template renders short forms")
	srv := newServer(t)
	// ...rest unchanged
}
```

- [ ] **Step 2: Run tests to verify the validation tests fail**

Run: `go test ./internal/mastermind/httpserver/... -run "InvalidJiraURL|InvalidPRURL"`

Expected: FAIL — `parseTaskForm` doesn't read the new fields yet, so the POSTs return 303 instead of 400.

- [ ] **Step 3: Extend `taskForm`, `parseTaskForm`, `formFromTask`**

In `internal/mastermind/httpserver/handlers.go`:

1. Add the import:

```go
import (
	// ... existing imports ...
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/issuerefs"
)
```

2. Extend `taskForm`:

```go
type taskForm struct {
	Name         string
	Priority     int
	MaxAttempts  int
	ScheduledFor string
	Payload      string
	JiraURL      string
	GithubPRURL  string
}
```

3. Extend `parseTaskForm`. Replace the existing body with:

```go
func parseTaskForm(r *http.Request) (taskForm, store.NewTaskInput, error) {
	if err := r.ParseForm(); err != nil {
		return taskForm{}, store.NewTaskInput{}, err
	}
	f := taskForm{
		Name:         r.FormValue("name"),
		ScheduledFor: r.FormValue("scheduled_for"),
		Payload:      r.FormValue("payload"),
		JiraURL:      strings.TrimSpace(r.FormValue("jira_url")),
		GithubPRURL:  strings.TrimSpace(r.FormValue("github_pr_url")),
	}
	if s := r.FormValue("priority"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("priority must be an integer")
		}
		f.Priority = n
	}
	if s := r.FormValue("max_attempts"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("max_attempts must be an integer")
		}
		f.MaxAttempts = n
	}
	if f.MaxAttempts <= 0 {
		f.MaxAttempts = 3
	}
	if f.Payload == "" {
		f.Payload = "{}"
	}
	if !json.Valid([]byte(f.Payload)) {
		return f, store.NewTaskInput{}, errors.New("payload must be valid JSON")
	}
	payloadJSON := json.RawMessage(f.Payload)

	var jiraPtr, prPtr *string
	if f.JiraURL != "" {
		if err := issuerefs.ValidateJira(f.JiraURL); err != nil {
			return f, store.NewTaskInput{}, errors.New("JIRA URL must look like https://<host>/browse/PROJ-123")
		}
		v := f.JiraURL
		jiraPtr = &v
	}
	if f.GithubPRURL != "" {
		if err := issuerefs.ValidatePR(f.GithubPRURL); err != nil {
			return f, store.NewTaskInput{}, errors.New("GitHub PR URL must look like https://<host>/<org>/<repo>/pull/123")
		}
		v := f.GithubPRURL
		prPtr = &v
	}

	input := store.NewTaskInput{
		Name:        f.Name,
		Priority:    f.Priority,
		MaxAttempts: f.MaxAttempts,
		Payload:     payloadJSON,
		JiraURL:     jiraPtr,
		GithubPRURL: prPtr,
	}
	if f.ScheduledFor != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", f.ScheduledFor, time.Local)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("scheduled_for must be YYYY-MM-DDTHH:MM")
		}
		input.ScheduledFor = &t
	}
	return f, input, nil
}
```

4. Extend `formFromTask`:

```go
func formFromTask(t store.Task) taskForm {
	tf := taskForm{
		Name:        t.Name,
		Priority:    t.Priority,
		MaxAttempts: t.MaxAttempts,
		Payload:     string(t.Payload),
	}
	if t.ScheduledFor != nil {
		tf.ScheduledFor = t.ScheduledFor.In(time.Local).Format("2006-01-02T15:04")
	}
	if t.JiraURL != nil {
		tf.JiraURL = *t.JiraURL
	}
	if t.GithubPRURL != nil {
		tf.GithubPRURL = *t.GithubPRURL
	}
	return tf
}
```

5. Propagate into `tasksUpdate`. Replace the store call:

```go
	_, err = s.store.UpdateTask(r.Context(), id, store.UpdateTaskInput{
		Name: input.Name, Priority: input.Priority,
		MaxAttempts: input.MaxAttempts, ScheduledFor: input.ScheduledFor,
		Payload:     input.Payload,
		JiraURL:     input.JiraURL,
		GithubPRURL: input.GithubPRURL,
	})
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS for the two validation tests. `TestCreateTask_WithIssueRefs` remains skipped.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/httpserver/handlers.go internal/mastermind/httpserver/handlers_test.go
git commit -m "feat(httpserver): parse and validate issue-ref URLs in task form"
```

---

## Task 9: `POST /tasks/{id}/links` handler + route (TDD)

**Files:**
- Modify: `internal/mastermind/httpserver/handlers.go`
- Modify: `internal/mastermind/httpserver/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/httpserver/handlers_test.go`:

```go
func TestUpdateLinks_WorksOnFailedTask(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task, err := s.CreateTask(context.Background(), store.NewTaskInput{Name: "x", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Force the task into "failed".
	if _, err := s.Pool().Exec(context.Background(), `UPDATE tasks SET status='failed' WHERE id=$1`, task.ID); err != nil {
		t.Fatalf("force status: %v", err)
	}

	form := url.Values{
		"jira_url":      {"https://acme.atlassian.net/browse/PROJ-9"},
		"github_pr_url": {"https://github.com/acme/widget/pull/42"},
	}.Encode()

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/links", form))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.JiraURL == nil || *got.JiraURL != "https://acme.atlassian.net/browse/PROJ-9" {
		t.Errorf("JiraURL not saved: %v", got.JiraURL)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != "https://github.com/acme/widget/pull/42" {
		t.Errorf("GithubPRURL not saved: %v", got.GithubPRURL)
	}
}

func TestUpdateLinks_InvalidJira(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	task, _ := s.CreateTask(context.Background(), store.NewTaskInput{Name: "x", MaxAttempts: 3})

	form := url.Values{"jira_url": {"nope"}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/links", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mastermind/httpserver/... -run UpdateLinks`

Expected: FAIL — the route isn't wired, so the requests 404.

- [ ] **Step 3: Wire the route verb**

In `internal/mastermind/httpserver/handlers.go`, extend the `tasksMember` switch. Add one case just before the `default`:

```go
	case verb == "links" && r.Method == http.MethodPost:
		s.tasksUpdateLinks(w, r, id)
```

Then add the handler (place it near `tasksUpdate`):

```go
func (s *Server) tasksUpdateLinks(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jira := strings.TrimSpace(r.FormValue("jira_url"))
	pr := strings.TrimSpace(r.FormValue("github_pr_url"))

	var jiraPtr, prPtr *string
	if jira != "" {
		if err := issuerefs.ValidateJira(jira); err != nil {
			http.Error(w, "JIRA URL must look like https://<host>/browse/PROJ-123", http.StatusBadRequest)
			return
		}
		jiraPtr = &jira
	}
	if pr != "" {
		if err := issuerefs.ValidatePR(pr); err != nil {
			http.Error(w, "GitHub PR URL must look like https://<host>/<org>/<repo>/pull/123", http.StatusBadRequest)
			return
		}
		prPtr = &pr
	}

	if _, err := s.store.UpdateTaskLinks(r.Context(), id, jiraPtr, prPtr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/tasks/"+id.String(), http.StatusSeeOther)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS for the two new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/httpserver/handlers.go internal/mastermind/httpserver/handlers_test.go
git commit -m "feat(httpserver): POST /tasks/{id}/links updates refs in any status"
```

---

## Task 10: New-task template — add inputs

**Files:**
- Modify: `internal/mastermind/httpserver/templates/tasks_new.html`

- [ ] **Step 1: Add inputs below the existing fields**

Replace the form in `tasks_new.html` with:

```html
{{ define "content" }}
<h1>New task</h1>
{{ if .Error }}<p role="alert">{{ .Error }}</p>{{ end }}
<form method="post" action="/tasks">
  <label>Name <input name="name" required value="{{ .Form.Name }}"></label>
  <label>Priority <input type="number" name="priority" value="{{ .Form.Priority }}"></label>
  <label>Max attempts <input type="number" name="max_attempts" value="{{ .Form.MaxAttempts }}" min="1"></label>
  <label>Scheduled for <input type="datetime-local" name="scheduled_for" value="{{ .Form.ScheduledFor }}"></label>
  <label>JIRA URL
    <input type="url" name="jira_url"
           placeholder="https://acme.atlassian.net/browse/PROJ-123"
           value="{{ .Form.JiraURL }}">
  </label>
  <label>GitHub PR URL
    <input type="url" name="github_pr_url"
           placeholder="https://github.com/org/repo/pull/123"
           value="{{ .Form.GithubPRURL }}">
  </label>
  <label>Payload (JSON) <textarea name="payload" rows="6">{{ .Form.Payload }}</textarea></label>
  <button type="submit">Create</button>
</form>
{{ end }}
```

- [ ] **Step 2: Run the existing create/list tests**

Run: `go test ./internal/mastermind/httpserver/... -run "CreateAndListTask|CreateTask_Invalid"`

Expected: PASS — the form accepts the extra (optional) fields as empty on existing flows.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/httpserver/templates/tasks_new.html
git commit -m "feat(ui): add JIRA and GitHub PR inputs to task create form"
```

---

## Task 11: Edit-task template — add inputs

**Files:**
- Modify: `internal/mastermind/httpserver/templates/tasks_edit.html`

- [ ] **Step 1: Mirror the inputs on the edit form**

Replace the file with:

```html
{{ define "content" }}
<h1>Edit task</h1>
{{ if .Error }}<p role="alert">{{ .Error }}</p>{{ end }}
<form method="post" action="/tasks/{{ .Task.ID }}">
  <label>Name <input name="name" required value="{{ .Form.Name }}"></label>
  <label>Priority <input type="number" name="priority" value="{{ .Form.Priority }}"></label>
  <label>Max attempts <input type="number" name="max_attempts" value="{{ .Form.MaxAttempts }}" min="1"></label>
  <label>Scheduled for <input type="datetime-local" name="scheduled_for" value="{{ .Form.ScheduledFor }}"></label>
  <label>JIRA URL
    <input type="url" name="jira_url"
           placeholder="https://acme.atlassian.net/browse/PROJ-123"
           value="{{ .Form.JiraURL }}">
  </label>
  <label>GitHub PR URL
    <input type="url" name="github_pr_url"
           placeholder="https://github.com/org/repo/pull/123"
           value="{{ .Form.GithubPRURL }}">
  </label>
  <label>Payload (JSON) <textarea name="payload" rows="6">{{ .Form.Payload }}</textarea></label>
  <button type="submit">Save</button>
  <a href="/tasks/{{ .Task.ID }}" role="button" class="secondary">Cancel</a>
</form>
{{ end }}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/mastermind/httpserver/templates/tasks_edit.html
git commit -m "feat(ui): add JIRA and GitHub PR inputs to task edit form"
```

---

## Task 12: List fragment — render `Refs` column + un-skip round-trip test

**Files:**
- Modify: `internal/mastermind/httpserver/templates/tasks_fragment.html`
- Modify: `internal/mastermind/httpserver/handlers_test.go`

- [ ] **Step 1: Add the `Refs` column**

Replace the contents of `tasks_fragment.html`:

```html
{{ define "tasks_fragment" }}
<table role="grid">
  <thead>
    <tr>
      <th>Name</th><th>Status</th><th>Priority</th><th>Attempts</th>
      <th>Claimed by</th><th>Refs</th><th>Updated</th><th></th>
    </tr>
  </thead>
  <tbody>
    {{ range .Items }}
      <tr>
        <td><a href="/tasks/{{ .ID }}">{{ .Name }}</a></td>
        <td>{{ .Status }}</td>
        <td>{{ .Priority }}</td>
        <td>{{ .AttemptCount }} / {{ .MaxAttempts }}</td>
        <td>{{ if .ClaimedBy }}{{ .ClaimedBy }}{{ else }}—{{ end }}</td>
        <td>
          {{ if .JiraURL }}
            <a href="{{ .JiraURL }}" target="_blank" rel="noopener noreferrer">{{ jiraShort (deref .JiraURL) }}</a>
          {{ end }}
          {{ if .GithubPRURL }}
            <a href="{{ .GithubPRURL }}" target="_blank" rel="noopener noreferrer">{{ prShort (deref .GithubPRURL) }}</a>
          {{ end }}
          {{ if and (not .JiraURL) (not .GithubPRURL) }}—{{ end }}
        </td>
        <td>{{ .UpdatedAt.Format "2006-01-02 15:04:05" }}</td>
        <td>
          {{ if or (eq .Status "failed") (eq .Status "completed") }}
            <form method="post" action="/tasks/{{ .ID }}/requeue" hx-post="/tasks/{{ .ID }}/requeue" hx-target="#task-table">
              <button type="submit" class="secondary">requeue</button>
            </form>
          {{ end }}
        </td>
      </tr>
    {{ else }}
      <tr><td colspan="8"><em>no tasks</em></td></tr>
    {{ end }}
  </tbody>
</table>
<p>{{ .Total }} total</p>
{{ end }}
```

Two notes:
- `Task.JiraURL` is `*string`; templates need the dereferenced value to pass through the `jiraShort` / `prShort` funcs. Add a `deref` template func that turns `*string` into `string`. Register it alongside the others.
- The empty-row `colspan` moves from 7 to 8.

- [ ] **Step 2: Register `deref` as a template func**

In `internal/mastermind/httpserver/server.go`, extend the `funcs` map:

```go
funcs := template.FuncMap{
    "jiraShort": issuerefs.JiraShort,
    "prShort":   issuerefs.PRShort,
    "deref": func(s *string) string {
        if s == nil {
            return ""
        }
        return *s
    },
}
```

- [ ] **Step 3: Un-skip `TestCreateTask_WithIssueRefs`**

In `internal/mastermind/httpserver/handlers_test.go`, remove the `t.Skip(...)` line from `TestCreateTask_WithIssueRefs` (leave the rest unchanged).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS — round-trip test now sees `PROJ-1` and `acme/widget#7` in the rendered list.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/httpserver/templates/tasks_fragment.html internal/mastermind/httpserver/server.go internal/mastermind/httpserver/handlers_test.go
git commit -m "feat(ui): show JIRA/PR short-form badges in task list"
```

---

## Task 13: Detail page — render links + inline update form (TDD)

**Files:**
- Modify: `internal/mastermind/httpserver/templates/tasks_detail.html`
- Modify: `internal/mastermind/httpserver/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/mastermind/httpserver/handlers_test.go`:

```go
func TestDetailPage_ShowsRefsAndUpdateForm(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()

	jira := "https://acme.atlassian.net/browse/PROJ-5"
	pr := "https://github.com/acme/widget/pull/11"
	task, _ := s.CreateTask(context.Background(), store.NewTaskInput{
		Name: "detail", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})
	// Force a non-pending status to prove the update form is still shown.
	if _, err := s.Pool().Exec(context.Background(), `UPDATE tasks SET status='failed' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks/"+task.ID.String(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PROJ-5") {
		t.Errorf("detail missing JIRA short form: %s", body)
	}
	if !strings.Contains(body, "acme/widget#11") {
		t.Errorf("detail missing PR short form: %s", body)
	}
	if !strings.Contains(body, `action="/tasks/`+task.ID.String()+`/links"`) {
		t.Errorf("detail missing link-update form: %s", body)
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/mastermind/httpserver/... -run ShowsRefsAndUpdateForm`

Expected: FAIL — template doesn't render the short forms or the form yet.

- [ ] **Step 3: Update the detail template**

Replace `tasks_detail.html` with:

```html
{{ define "content" }}
<h1>{{ .Task.Name }}</h1>
<p><strong>Status:</strong> {{ .Task.Status }}</p>
<p><strong>Priority:</strong> {{ .Task.Priority }}</p>
<p><strong>Attempts:</strong> {{ .Task.AttemptCount }} / {{ .Task.MaxAttempts }}</p>
<p><strong>Claimed by:</strong> {{ if .Task.ClaimedBy }}{{ .Task.ClaimedBy }}{{ else }}—{{ end }}</p>
<p><strong>Last heartbeat:</strong> {{ if .Task.LastHeartbeatAt }}{{ .Task.LastHeartbeatAt }}{{ else }}—{{ end }}</p>
<p><strong>Last error:</strong> {{ if .Task.LastError }}<code>{{ .Task.LastError }}</code>{{ else }}—{{ end }}</p>
<p>
  <strong>JIRA:</strong>
  {{ if .Task.JiraURL }}
    <a href="{{ deref .Task.JiraURL }}" target="_blank" rel="noopener noreferrer">{{ jiraShort (deref .Task.JiraURL) }}</a>
  {{ else }}—{{ end }}
</p>
<p>
  <strong>GitHub PR:</strong>
  {{ if .Task.GithubPRURL }}
    <a href="{{ deref .Task.GithubPRURL }}" target="_blank" rel="noopener noreferrer">{{ prShort (deref .Task.GithubPRURL) }}</a>
  {{ else }}—{{ end }}
</p>
<pre>{{ printf "%s" .Task.Payload }}</pre>

<details>
  <summary>Update links</summary>
  <form method="post" action="/tasks/{{ .Task.ID }}/links">
    <label>JIRA URL
      <input type="url" name="jira_url"
             placeholder="https://acme.atlassian.net/browse/PROJ-123"
             value="{{ deref .Task.JiraURL }}">
    </label>
    <label>GitHub PR URL
      <input type="url" name="github_pr_url"
             placeholder="https://github.com/org/repo/pull/123"
             value="{{ deref .Task.GithubPRURL }}">
    </label>
    <button type="submit">Save links</button>
  </form>
</details>

<div class="grid">
  {{ if eq .Task.Status "pending" }}
    <a href="/tasks/{{ .Task.ID }}/edit" role="button">Edit</a>
  {{ end }}
  {{ if or (eq .Task.Status "completed") (eq .Task.Status "failed") }}
    <form method="post" action="/tasks/{{ .Task.ID }}/requeue">
      <button type="submit">Requeue</button>
    </form>
  {{ end }}
  {{ if ne .Task.Status "claimed" }}{{ if ne .Task.Status "running" }}
    <form method="post" action="/tasks/{{ .Task.ID }}/delete"
          onsubmit="return confirm('Delete this task?');">
      <button type="submit" class="contrast">Delete</button>
    </form>
  {{ end }}{{ end }}
</div>
<p><a href="/tasks">← back</a></p>
{{ end }}
```

The Update links `<details>` block is rendered for every status — that's the point.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/mastermind/httpserver/...`

Expected: PASS for the new test plus everything else.

- [ ] **Step 5: Commit**

```bash
git add internal/mastermind/httpserver/templates/tasks_detail.html internal/mastermind/httpserver/handlers_test.go
git commit -m "feat(ui): show refs on detail page with inline update form"
```

---

## Task 14: Full test sweep

Gate to make sure nothing regressed.

- [ ] **Step 1: Run all tests**

Run: `go test ./...`

Expected: PASS everywhere.

- [ ] **Step 2: `go vet`**

Run: `go vet ./...`

Expected: no output.

- [ ] **Step 3: If anything fails, diagnose, fix, and return to Step 1.** Otherwise, proceed.

---

## Task 15: Manual UI smoke test

**Files:** none (manual).

Before marking the feature complete, drive the admin UI with a real browser.

- [ ] **Step 1: Bring up the stack**

Run: `docker compose up -d` (or the project's equivalent — see README).

- [ ] **Step 2: Exercise each path**

Navigate to `/tasks/new`. Verify:
1. Creating a task with both URL fields empty works (baseline regression).
2. Creating a task with valid URLs shows the short-form badges in the list.
3. Clicking a badge opens the full URL in a new tab (check `target="_blank"`).
4. Creating a task with an invalid JIRA URL shows the error banner (form re-rendered).
5. Editing a pending task via `Edit` carries the URLs through and allows clearing them.
6. On a failed task (force with SQL: `UPDATE tasks SET status='failed' WHERE ...`), the detail page still shows the `Update links` form; submitting it saves the URLs and redirects back.

- [ ] **Step 3: Note any findings**

If anything looks off, open an issue or return to the relevant task.

---

## Notes

- The gRPC `Task` proto in `internal/proto/tasks.proto` is deliberately unchanged; workers do not need these fields.
- Tests use `testcontainers-go` to bring up a fresh Postgres per package, so the new migration is applied automatically — no local DB setup is required.
- `strings` is already imported by `handlers.go`; no import changes are needed there beyond adding `issuerefs`.
- `uuid` is already imported by `tasks_update_test.go` once Task 5 adds it to the import block.
- **HTMX on the links update form is deliberately skipped.** The spec mentioned
  "HTMX request → re-render the detail-page links fragment", but v1 uses a plain
  form POST + 303 redirect. There's no compelling reason to avoid the full-page
  reload on the detail view, and shipping the HTMX variant would require a new
  `links_fragment` template and another parse entry in `server.go` for no user
  benefit. Add it later if/when the detail page grows heavier.
