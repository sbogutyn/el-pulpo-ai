package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
