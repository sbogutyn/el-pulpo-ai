package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

// startMCPClient wires the worker MCP server to an in-memory client session.
func startMCPClient(t *testing.T, st *State) *mcp.ClientSession {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := NewServer(st)
	go func() { _ = srv.Run(context.Background(), serverT) }()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := c.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestTool_ClaimNext_EmptyQueueIsError(t *testing.T) {
	fx := newWorkerFixture(t)
	session := startMCPClient(t, fx.state)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "claim_next_task",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("want IsError=true on empty queue")
	}
}

func TestTool_EndToEnd_ClaimProgressLogComplete(t *testing.T) {
	fx := newWorkerFixture(t)
	seedTask(t, fx, "job-A")
	session := startMCPClient(t, fx.state)

	// claim_next_task
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "claim_next_task",
		Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("claim_next_task: err=%v res=%+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var view TaskView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Name != "job-A" || view.ID == "" {
		t.Fatalf("unexpected view: %+v", view)
	}

	// update_progress
	res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "update_progress",
		Arguments: map[string]any{"note": "halfway"},
	})
	if res.IsError {
		t.Fatalf("update_progress: %+v", res.Content)
	}

	// append_log twice
	for _, msg := range []string{"first", "second"} {
		res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "append_log",
			Arguments: map[string]any{"message": msg},
		})
		if res.IsError {
			t.Fatalf("append_log %q: %+v", msg, res.Content)
		}
	}

	// get_current_task returns the same id
	res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_current_task",
		Arguments: map[string]any{},
	})
	if res.IsError {
		t.Fatalf("get_current_task: %+v", res.Content)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var cur TaskView
	_ = json.Unmarshal(raw, &cur)
	if cur.ID != view.ID {
		t.Errorf("current id=%q, want %q", cur.ID, view.ID)
	}

	// complete_task
	res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "complete_task",
		Arguments: map[string]any{},
	})
	if res.IsError {
		t.Fatalf("complete_task: %+v", res.Content)
	}

	// get_current_task should now error
	res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_current_task",
		Arguments: map[string]any{},
	})
	if !res.IsError {
		t.Fatal("want IsError=true when idle")
	}
}

func TestTool_FailTask_MissingMessageIsToolError(t *testing.T) {
	fx := newWorkerFixture(t)
	seedTask(t, fx, "t")
	session := startMCPClient(t, fx.state)

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fail_task",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("want IsError=true for missing message")
	}
}

func TestTool_ClaimNext_IdempotentWhileHolding(t *testing.T) {
	fx := newWorkerFixture(t)
	seedTask(t, fx, "job-A")
	seedTask(t, fx, "job-B")
	session := startMCPClient(t, fx.state)

	// First call claims job-A.
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "claim_next_task"})
	raw, _ := json.Marshal(res.StructuredContent)
	var v1 TaskView
	_ = json.Unmarshal(raw, &v1)

	// Second call returns the same task (not an error, not a fresh claim).
	res, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "claim_next_task"})
	if res.IsError {
		t.Fatalf("second claim: %+v", res.Content)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var v2 TaskView
	_ = json.Unmarshal(raw, &v2)
	if v1.ID != v2.ID {
		t.Errorf("ids differ: %q vs %q", v1.ID, v2.ID)
	}
}

func TestTool_SetJiraURL_RequiresURL(t *testing.T) {
	fx := newWorkerFixture(t)
	seedTask(t, fx, "job-A")
	session := startMCPClient(t, fx.state)

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task", Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}

	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "set_jira_url",
		Arguments: map[string]any{"url": ""},
	})
	if !res.IsError {
		t.Error("expected tool error for empty url")
	}
}

func TestTool_SetJiraURL_HappyPath(t *testing.T) {
	fx := newWorkerFixture(t)
	id := seedTask(t, fx, "job-A")
	session := startMCPClient(t, fx.state)

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task", Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}

	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "set_jira_url",
		Arguments: map[string]any{"url": "https://jira/T-1"},
	})
	if res.IsError {
		t.Fatalf("set_jira_url: %+v", res.Content)
	}

	got, _ := fx.store.GetTask(context.Background(), id)
	if got.JiraURL == nil || *got.JiraURL != "https://jira/T-1" {
		t.Errorf("jira_url=%v, want https://jira/T-1", got.JiraURL)
	}
}

func TestTool_OpenPR_RequiresURL(t *testing.T) {
	fx := newWorkerFixture(t)
	seedTask(t, fx, "job-A")
	session := startMCPClient(t, fx.state)

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task", Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}

	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "open_pr",
		Arguments: map[string]any{"github_pr_url": ""},
	})
	if !res.IsError {
		t.Error("expected tool error for empty github_pr_url")
	}
}

func TestTool_OpenPR_HappyPath(t *testing.T) {
	fx := newWorkerFixture(t)
	id := seedTask(t, fx, "job-A")
	session := startMCPClient(t, fx.state)

	// claim then heartbeat (via update_progress) so server reaches in_progress.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task", Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "update_progress", Arguments: map[string]any{"note": "starting"},
	}); err != nil {
		t.Fatal(err)
	}

	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "open_pr",
		Arguments: map[string]any{"github_pr_url": "https://github.com/o/r/pull/1"},
	})
	if res.IsError {
		t.Fatalf("open_pr: %+v", res.Content)
	}

	// State must be cleared.
	if _, err := fx.state.Current(); !errors.Is(err, ErrNoCurrentTask) {
		t.Errorf("Current err=%v, want ErrNoCurrentTask", err)
	}
	// Server-side must be in pr_opened with the URL set.
	got, _ := fx.store.GetTask(context.Background(), id)
	if got.Status != store.StatusPROpened {
		t.Errorf("status=%q, want pr_opened", got.Status)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != "https://github.com/o/r/pull/1" {
		t.Errorf("github_pr_url=%v, want https://github.com/o/r/pull/1", got.GithubPRURL)
	}
}

func TestTool_ClaimNext_SurfacesInstructions(t *testing.T) {
	fx := newWorkerFixture(t)
	created, err := fx.store.CreateTask(context.Background(), store.NewTaskInput{
		Name:        "withInstr",
		MaxAttempts: 3,
		Payload:     json.RawMessage(`{"instructions":"do the thing"}`),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_ = created

	session := startMCPClient(t, fx.state)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "claim_next_task", Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("claim_next_task: err=%v res=%+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var view TaskView
	_ = json.Unmarshal(raw, &view)
	if view.Instructions != "do the thing" {
		t.Errorf("instructions=%q, want %q", view.Instructions, "do the thing")
	}
}
