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
