//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func TestMMCP_ToolsList(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectMastermindMCP(t, S.AdminToken)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"create_task":    false,
		"get_task":       false,
		"list_tasks":     false,
		"request_review": false,
		"finalize_task":  false,
	}
	for _, tool := range list.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("mastermind-mcp missing tool %q", name)
		}
	}
}

func TestMMCP_CreateAndGet(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectMastermindMCP(t, S.AdminToken)

	name := "mmcp-create-" + shortID()
	res := callTool(t, sess, "create_task", map[string]any{
		"name":     name,
		"priority": 3,
		"payload":  map[string]any{"from": "mmcp", "instructions": "drive the mastermind-mcp test"},
	})
	if res.IsError {
		t.Fatalf("create_task: %q", toolText(res))
	}
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeStructured(res, &out); err != nil {
		t.Fatalf("decode create_task: %v (text=%q)", err, toolText(res))
	}
	if out.ID == "" || out.Name != name {
		t.Fatalf("create_task unexpected: %+v (text=%q)", out, toolText(res))
	}

	// get_task happy path.
	got := callTool(t, sess, "get_task", map[string]any{"id": out.ID})
	if got.IsError {
		t.Fatalf("get_task: %q", toolText(got))
	}
	var back struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := decodeStructured(got, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != out.ID {
		t.Errorf("get_task id=%q want %q", back.ID, out.ID)
	}
	if back.Status != "pending" {
		t.Errorf("status=%q want pending", back.Status)
	}

	// get_task not-found.
	notfound := callTool(t, sess, "get_task", map[string]any{"id": "00000000-0000-0000-0000-000000000000"})
	if !notfound.IsError {
		t.Fatalf("get_task on unknown id should be IsError=true (text=%q)", toolText(notfound))
	}
}

func TestMMCP_CreateInvalid(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectMastermindMCP(t, S.AdminToken)

	// Missing name.
	res := callTool(t, sess, "create_task", map[string]any{})
	if !res.IsError {
		t.Fatalf("empty-name create_task should be IsError=true; text=%q", toolText(res))
	}
	if !strings.Contains(toolText(res), "name") {
		t.Errorf("error text=%q should mention 'name'", toolText(res))
	}
}

func TestMMCP_List(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectMastermindMCP(t, S.AdminToken)

	// Seed one task so list has at least one entry.
	seed := "mmcp-list-" + shortID()
	_ = callTool(t, sess, "create_task", map[string]any{
		"name":    seed,
		"payload": map[string]any{"instructions": "list me"},
	})

	res := callTool(t, sess, "list_tasks", map[string]any{"limit": 500})
	if res.IsError {
		t.Fatalf("list_tasks: %q", toolText(res))
	}
	var out struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := decodeStructured(res, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total < 1 {
		t.Fatalf("total=%d want >=1", out.Total)
	}
	var found bool
	for _, it := range out.Items {
		if it.Name == seed {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("seeded task %q not in list; items=%d total=%d", seed, len(out.Items), out.Total)
	}
}

// TestMMCP_RequestReviewAndFinalize covers the new admin MCP tools end
// to end: park a task via worker gRPC, then drive it through
// request_review → finalize_task using the mastermind-mcp stdio binary.
func TestMMCP_RequestReviewAndFinalize(t *testing.T) {
	requireEndpointsReady(t)
	id, _ := parkTask(t, "https://github.com/org/repo/pull/400")

	sess := connectMastermindMCP(t, S.AdminToken)

	// request_review happy path: pr_opened → review_requested.
	res := callTool(t, sess, "request_review", map[string]any{"id": id})
	if res.IsError {
		t.Fatalf("request_review: %q", toolText(res))
	}
	var view struct {
		Status string `json:"status"`
	}
	if err := decodeStructured(res, &view); err != nil {
		t.Fatalf("decode request_review: %v", err)
	}
	if view.Status != "review_requested" {
		t.Errorf("status=%q want review_requested", view.Status)
	}

	// finalize_task with a malformed outcome must be a tool error.
	bad := callTool(t, sess, "finalize_task", map[string]any{"id": id, "outcome": "banana"})
	if !bad.IsError {
		t.Fatalf("finalize_task with bad outcome should be IsError=true; text=%q", toolText(bad))
	}

	// finalize_task happy path: review_requested → completed.
	done := callTool(t, sess, "finalize_task", map[string]any{
		"id":      id,
		"outcome": "success",
	})
	if done.IsError {
		t.Fatalf("finalize_task: %q", toolText(done))
	}
	var doneView struct {
		Status string `json:"status"`
	}
	if err := decodeStructured(done, &doneView); err != nil {
		t.Fatalf("decode finalize_task: %v", err)
	}
	if doneView.Status != "completed" {
		t.Errorf("status=%q want completed", doneView.Status)
	}
}

// TestMMCP_FinalizeTaskFailure covers the failure path of finalize_task,
// including the message propagating to last_error.
func TestMMCP_FinalizeTaskFailure(t *testing.T) {
	requireEndpointsReady(t)
	id, _ := parkTask(t, "https://github.com/org/repo/pull/401")

	sess := connectMastermindMCP(t, S.AdminToken)

	const reason = "rolled back via mmcp"
	res := callTool(t, sess, "finalize_task", map[string]any{
		"id":      id,
		"outcome": "failure",
		"message": reason,
	})
	if res.IsError {
		t.Fatalf("finalize_task: %q", toolText(res))
	}

	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "failed" {
		t.Errorf("status=%q want failed", got.GetTask().GetStatus())
	}
	if !strings.Contains(got.GetTask().GetLastError(), reason) {
		t.Errorf("last_error=%q want contains %q", got.GetTask().GetLastError(), reason)
	}
}

// TestMMCP_CreateRequiresInstructions covers the validator surfacing
// through the MCP layer: tool call returns IsError=true with a message
// that mentions instructions.
func TestMMCP_CreateRequiresInstructions(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectMastermindMCP(t, S.AdminToken)

	res := callTool(t, sess, "create_task", map[string]any{
		"name":    "mmcp-no-instr-" + shortID(),
		"payload": map[string]any{"k": "v"},
	})
	if !res.IsError {
		t.Fatalf("create_task without instructions should be IsError=true; text=%q", toolText(res))
	}
	if !strings.Contains(toolText(res), "instructions") {
		t.Errorf("error text=%q should mention 'instructions'", toolText(res))
	}
}

func TestMMCP_AuthFailure(t *testing.T) {
	requireEndpointsReady(t)
	// Spawn the binary with a wrong token — it probes mastermind at
	// startup and exits. The Connect call should fail.
	// We can't use connectMastermindMCP because it fails the test on
	// Connect error. Instead, build and run inline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := probeMMCPWithToken(ctx, "wrong-token"); err == nil {
		t.Fatal("expected mastermind-mcp to fail with bad token, got nil")
	}
}
