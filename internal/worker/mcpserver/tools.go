package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

// TaskView is the JSON shape the worker MCP tools return. It intentionally
// exposes only what the agent needs to do its work — not bookkeeping fields
// like priority, attempts, or lease timestamps.
type TaskView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	WorkerID     string `json:"worker_id"`
	Instructions string `json:"instructions,omitempty"`
	Payload      any    `json:"payload"`
	JiraURL      string `json:"jira_url,omitempty"`
	GithubPRURL  string `json:"github_pr_url,omitempty"`
}

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
		// Keep raw bytes as a string so the agent can still see the payload.
		v.Payload = string(raw)
	} else if m, ok := v.Payload.(map[string]any); ok {
		if s, ok := m["instructions"].(string); ok {
			v.Instructions = s
		}
	}
	return v
}

// NewServer builds an MCP server that exposes the worker's task surface to a
// coding agent. The returned server is un-started; callers attach it to a
// transport (e.g. [mcp.StreamableHTTPHandler]) to expose it over HTTP or
// stdio.
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

type claimNextInput struct{}

func registerClaimNext(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "claim_next_task",
		Description: "Claim the next task from the mastermind queue. " +
			"Returns the task the worker is now holding, or an error if the " +
			"queue is empty or a task is already claimed. Idempotent: if a " +
			"task is already held, returns it without claiming a new one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ claimNextInput) (*mcp.CallToolResult, TaskView, error) {
		task, err := st.ClaimNext(ctx)
		if errors.Is(err, ErrAlreadyHaveTask) {
			v := viewFromTask(task, st.client.WorkerID())
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf("Already holding task %s (%s); finalize it before claiming another.", v.ID, v.Name),
				}},
			}, v, nil
		}
		if errors.Is(err, taskclient.ErrNoTask) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "claim_next_task: no tasks available"}},
			}, TaskView{}, nil
		}
		if err != nil {
			return toolErr(err, "claim_next_task"), TaskView{}, nil
		}
		v := viewFromTask(task, st.client.WorkerID())
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Claimed task %s (%s)", v.ID, v.Name),
			}},
		}, v, nil
	})
}

type getCurrentInput struct{}

func registerGetCurrent(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_current_task",
		Description: "Return the task the worker is currently holding, or an error when idle.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ getCurrentInput) (*mcp.CallToolResult, TaskView, error) {
		task, err := st.Current()
		if err != nil {
			return toolErr(err, "get_current_task"), TaskView{}, nil
		}
		v := viewFromTask(task, st.client.WorkerID())
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("%s — %s", v.ID, v.Name),
			}},
		}, v, nil
	})
}

type updateProgressInput struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
	Note   string `json:"note" jsonschema:"short human-readable progress note (overwrites the previous note)"`
}

func registerUpdateProgress(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "update_progress",
		Description: "Set the short 'current status' note for the worker's task. " +
			"The note is overwritten on each call and surfaced in the mastermind admin UI. " +
			"Use append_log for an immutable narrative trail.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateProgressInput) (*mcp.CallToolResult, struct{}, error) {
		if err := st.Progress(ctx, in.TaskID, in.Note); err != nil {
			return toolErr(err, "update_progress"), struct{}{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "progress updated"}},
		}, struct{}{}, nil
	})
}

type appendLogInput struct {
	TaskID  string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
	Message string `json:"message" jsonschema:"free-form log line (required, non-empty)"`
}

type appendLogOutput struct {
	LogID int64 `json:"log_id"`
}

func registerAppendLog(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "append_log",
		Description: "Append one line to the task's append-only log. Useful to record " +
			"a narrative of what was done, what commands were run, links, etc.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appendLogInput) (*mcp.CallToolResult, appendLogOutput, error) {
		if in.Message == "" {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "append_log: message is required"}},
			}, appendLogOutput{}, nil
		}
		id, err := st.AppendLog(ctx, in.TaskID, in.Message)
		if err != nil {
			return toolErr(err, "append_log"), appendLogOutput{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("logged (id=%d)", id)}},
		}, appendLogOutput{LogID: id}, nil
	})
}

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

type completeTaskInput struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
}

func registerCompleteTask(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "complete_task",
		Description: "Mark the worker's task as successfully completed. " +
			"This releases the claim; the worker becomes idle and can claim another task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in completeTaskInput) (*mcp.CallToolResult, struct{}, error) {
		if err := st.Complete(ctx, in.TaskID); err != nil {
			return toolErr(err, "complete_task"), struct{}{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "task completed"}},
		}, struct{}{}, nil
	})
}

type failTaskInput struct {
	TaskID  string `json:"task_id,omitempty" jsonschema:"task id; defaults to the currently claimed task"`
	Message string `json:"message" jsonschema:"human-readable reason for failure (stored as last_error)"`
}

func registerFailTask(s *mcp.Server, st *State) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "fail_task",
		Description: "Mark the worker's task as failed with a message. Mastermind decides " +
			"whether to retry (linear backoff) or terminate based on the task's attempt budget.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in failTaskInput) (*mcp.CallToolResult, struct{}, error) {
		if in.Message == "" {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "fail_task: message is required"}},
			}, struct{}{}, nil
		}
		if err := st.Fail(ctx, in.TaskID, in.Message); err != nil {
			return toolErr(err, "fail_task"), struct{}{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "task failed"}},
		}, struct{}{}, nil
	})
}

// toolErr converts a State error (or gRPC-wrapped error from the taskclient)
// into an MCP tool error. We always return tool errors (IsError=true) rather
// than protocol errors — the MCP server itself should never fail a call just
// because a downstream call didn't.
func toolErr(err error, tool string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %s", tool, err.Error())}},
	}
}
