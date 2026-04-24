//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"
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
		"create_task": false,
		"get_task":    false,
		"list_tasks":  false,
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
		"payload":  map[string]any{"from": "mmcp"},
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
	_ = callTool(t, sess, "create_task", map[string]any{"name": seed})

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
