# Task issue references: JIRA ticket and GitHub PR

Add two optional reference links to each task so operators can trace a queued
unit of work back to the ticket that requested it and the PR that resolves it.

## Scope

Two new optional fields on `tasks`:

- `jira_url` — full URL to a JIRA issue.
- `github_pr_url` — full URL to a GitHub pull request.

Rendered in the admin UI as short-form badges (`PROJ-123`, `org/repo#3213`)
that open the full URL in a new tab when clicked. Tasks span many repos and
JIRA projects, so both values are stored as the full URL and the short form
is derived at render time.

Out of scope: exposing these fields to workers over gRPC; filtering or
searching tasks by reference; any backfill of existing rows (they stay NULL).

## Decisions

Resolved during brainstorming:

- **Per-task full URL** (not short form + global config). Tasks span many repos
  and JIRA projects; the admin pastes the URL they have.
- **GitHub PR short form is `org/repo#3213`** (not plain `#3213`) — the list
  view needs to disambiguate PRs from different repos at a glance. JIRA keys
  like `PROJ-123` are already globally unique.
- **Strict URL validation**. Both fields must match anchored regexes on save;
  typos fail at the form rather than producing broken links. Query strings
  and fragments are permitted (people commonly paste deep-links).
- **Reference fields are editable in any status.** Unlike name/payload/priority
  (pending-only), JIRA/PR links are documentation and frequently attached
  after the task has already run. A dedicated update path bypasses the
  pending-only rule.

## Data model

Migration `000002_add_task_issue_refs.{up,down}.sql`:

```sql
-- up
ALTER TABLE tasks
  ADD COLUMN jira_url      TEXT,
  ADD COLUMN github_pr_url TEXT;

-- down
ALTER TABLE tasks
  DROP COLUMN github_pr_url,
  DROP COLUMN jira_url;
```

Both columns are nullable; no CHECK constraints (validation is enforced in
Go at the single write path — the admin HTTP handlers); no indexes (not a
filter or sort target).

`store.Task`, `store.NewTaskInput`, and `store.UpdateTaskInput` each gain:

```go
JiraURL     *string `json:"jira_url,omitempty"`
GithubPRURL *string `json:"github_pr_url,omitempty"`
```

`taskColumns` and `scanTask` are extended to read/write both columns.
`CreateTask` and `UpdateTask` carry the fields through. `RequeueTask` leaves
them untouched — they are reference metadata, not execution state, so they
must survive a requeue.

A new store method backs the edit-in-any-status path:

```go
func (s *Store) UpdateTaskLinks(
    ctx context.Context, id uuid.UUID, jira, pr *string,
) (Task, error)
```

It issues `UPDATE tasks SET jira_url=$2, github_pr_url=$3, updated_at=now()
WHERE id=$1 RETURNING ...` with no status predicate, returning `ErrNotFound`
when no row matches. Nil pointers persist as SQL NULL, clearing the field.

## Validation and short-form rendering

New package `internal/mastermind/issuerefs` colocates validation and
rendering so there's a single source of truth for the allowed URL shapes:

```go
var jiraRE = regexp.MustCompile(`^https?://[^/]+/browse/([A-Z][A-Z0-9_]+-\d+)(?:[?#].*)?$`)
var prRE   = regexp.MustCompile(`^https?://[^/]+/([^/]+)/([^/]+)/pull/(\d+)(?:[?#].*)?$`)

var (
    ErrInvalidJira = errors.New("invalid JIRA URL")
    ErrInvalidPR   = errors.New("invalid GitHub PR URL")
)

func ValidateJira(s string) error
func ValidatePR(s string) error

func JiraShort(url string) string // "PROJ-123"; "" if url is empty
func PRShort(url string) string   // "org/repo#3213"; "" if url is empty
```

Anchoring with `(?:[?#].*)?$` allows `https://github.com/org/repo/pull/123?foo=bar`
and `.../pull/123#discussion_r1` without loosening the path match. Self-hosted
JIRA and GitHub Enterprise are supported because the host is not pinned.

The two `Short` functions are safe on any input: if the regex fails to
capture, they return the raw URL as a last-resort fallback. That's
defense-in-depth — the write path already validated — but keeps templates
tolerant of surprise inputs. Both are registered as template funcs
(`jiraShort`, `prShort`).

Table-driven tests cover: canonical URLs, self-hosted hosts, URLs with query
and fragment, missing scheme, wrong path, trailing slash, empty string.

## HTTP handlers and templates

`taskForm` gains two fields:

```go
type taskForm struct {
    Name, ScheduledFor, Payload string
    Priority, MaxAttempts       int
    JiraURL, GithubPRURL        string
}
```

`parseTaskForm` calls `issuerefs.ValidateJira` / `ValidatePR` when the
corresponding field is non-empty (empty is always valid — both fields are
optional). Validation errors re-render the form via the existing
`renderFormError` path.

`formFromTask` populates both fields from the persisted task.

Routing gains one verb inside `tasksMember`:

- `POST /tasks/{id}/links` → `tasksUpdateLinks(id)`.

`tasksUpdateLinks`:

1. Parses `jira_url` and `github_pr_url` from the form.
2. Validates non-empty values via `issuerefs`; on failure, returns 400 with
   the error message (HTMX target swaps in an error partial) or re-renders
   the detail page with an error banner for plain POSTs.
3. Converts empty strings to nil pointers.
4. Calls `store.UpdateTaskLinks`.
5. Responds: HTMX request → re-render the detail-page links fragment;
   plain POST → 303 redirect to `/tasks/{id}`.

No other handler changes for the pending-only edit flow — `tasksCreate` and
`tasksUpdate` already route through `parseTaskForm` and the existing store
methods, which now carry the new fields.

### Templates

- `tasks_new.html`, `tasks_edit.html`: two optional `<input type="url">`
  fields ("JIRA URL", "GitHub PR URL") below the existing inputs, with
  placeholder text showing the expected shape.
- `tasks_list.html`, `tasks_fragment.html`: one new `Refs` column rendering
  short-form badges for whichever fields are set:
  ```html
  {{ if .JiraURL }}
    <a href="{{ .JiraURL }}" target="_blank" rel="noopener noreferrer">
      {{ jiraShort .JiraURL }}
    </a>
  {{ end }}
  {{ if .GithubPRURL }}
    <a href="{{ .GithubPRURL }}" target="_blank" rel="noopener noreferrer">
      {{ prShort .GithubPRURL }}
    </a>
  {{ end }}
  ```
  Empty cell when neither is set.
- `tasks_detail.html`: show both as links in the task detail block; below
  them, a small inline form that POSTs to `/tasks/{id}/links` so operators
  can attach or change the refs in any status (including completed and
  failed). All outbound links use `target="_blank" rel="noopener noreferrer"`.

## gRPC

No change. `proto.Task` stays `{id, name, payload}`. These fields are
admin-only metadata; workers do not need them, and leaking them into the
wire contract would expand the surface area without value.

## Tests

- `internal/mastermind/issuerefs`: table-driven unit tests for all four
  functions as described above.
- `internal/mastermind/store`:
  - Create with both fields set, read back, compare.
  - `UpdateTask` updates both fields while task is pending.
  - `UpdateTaskLinks` works for tasks in `running`, `completed`, and `failed`.
  - `UpdateTaskLinks` with nil pointers clears the columns.
  - `RequeueTask` preserves both fields.
- `internal/mastermind/httpserver`:
  - Happy-path `POST /tasks` with both fields renders on list and detail.
  - Form validation error is surfaced in the re-rendered HTML.
  - `POST /tasks/{id}/links` on a `failed` task updates the fields.
  - HTMX variant of the links update returns the fragment.

## Rollout

Single migration, single build, single deploy. No feature flag; both columns
default to NULL so existing behavior is preserved for every pre-existing row.
The admin UI shows empty badges until an operator fills them in.
