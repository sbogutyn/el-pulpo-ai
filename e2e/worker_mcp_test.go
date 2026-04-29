//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// workerMCP_drain ensures the worker has no held claim before the next
// test. Finding out whether the worker holds one requires calling
// get_current_task; finalizing is complete_task.
func workerMCP_drain(t *testing.T) {
	t.Helper()
	// Establish a fresh session (previous cleanup closes it).
	sess := connectWorkerMCP(t)
	res := callTool(t, sess, "get_current_task", struct{}{})
	if res.IsError {
		return // idle
	}
	// Held — complete it.
	_ = callTool(t, sess, "complete_task", struct{}{})
}

func TestWorkerMCP_ToolsList(t *testing.T) {
	requireEndpointsReady(t)
	sess := connectWorkerMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"claim_next_task":  false,
		"get_current_task": false,
		"update_progress":  false,
		"append_log":       false,
		"set_jira_url":     false,
		"open_pr":          false,
		"complete_task":    false,
		"fail_task":        false,
	}
	for _, tool := range list.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("worker MCP missing tool %q", name)
		}
	}
}

func TestWorkerMCP_ClaimEmpty(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)

	// Drain the queue of any other pending tasks by calling claim +
	// complete until claim errors with 'no tasks available'.
	sess := connectWorkerMCP(t)
	for i := 0; i < 30; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			if strings.Contains(toolText(res), "no tasks available") {
				return
			}
			t.Fatalf("unexpected claim_next_task error: %q", toolText(res))
		}
		_ = callTool(t, sess, "complete_task", struct{}{})
	}
	t.Fatalf("queue never drained after 30 claim+complete iterations")
}

func TestWorkerMCP_ClaimAndComplete(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "worker-mcp-claim-" + shortID()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: name, MaxAttempts: 2, Payload: instructionsPayload(nil)})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	sess := connectWorkerMCP(t)

	// claim_next_task until we get our task — the queue may have other
	// pending items from earlier tests.
	var claimedID string
	for i := 0; i < 20; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			// Empty queue? shouldn't happen — we just created one.
			t.Fatalf("claim_next_task failed: %q", toolText(res))
		}
		var view struct {
			ID string `json:"id"`
		}
		if err := decodeStructured(res, &view); err != nil {
			t.Fatalf("decode claim_next_task: %v (text=%q)", err, toolText(res))
		}
		if view.ID == id {
			claimedID = id
			break
		}
		// Stray; complete and retry.
		_ = callTool(t, sess, "complete_task", struct{}{})
	}
	if claimedID == "" {
		t.Fatalf("never claimed %s", id)
	}

	// get_current_task returns the claimed task.
	cur := callTool(t, sess, "get_current_task", struct{}{})
	if cur.IsError {
		t.Fatalf("get_current_task tool error: %q", toolText(cur))
	}

	// update_progress.
	upd := callTool(t, sess, "update_progress", map[string]any{"note": "working hard"})
	if upd.IsError {
		t.Fatalf("update_progress: %q", toolText(upd))
	}

	// append_log twice.
	for _, msg := range []string{"mcp step 1", "mcp step 2"} {
		log := callTool(t, sess, "append_log", map[string]any{"message": msg})
		if log.IsError {
			t.Fatalf("append_log(%q): %q", msg, toolText(log))
		}
	}

	// complete_task.
	done := callTool(t, sess, "complete_task", struct{}{})
	if done.IsError {
		t.Fatalf("complete_task: %q", toolText(done))
	}

	// Admin confirms final state.
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "completed" {
		t.Errorf("status=%s want completed", got.GetTask().GetStatus())
	}

	// Admin confirms the logs landed, in order.
	logs, err := admin.ListTaskLogs(ctx, &pb.ListTaskLogsRequest{Id: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.GetItems()) < 2 {
		t.Fatalf("expected 2 log lines, got %d", len(logs.GetItems()))
	}
	last2 := logs.GetItems()[len(logs.GetItems())-2:]
	if !strings.Contains(last2[0].GetMessage(), "mcp step 1") || !strings.Contains(last2[1].GetMessage(), "mcp step 2") {
		t.Fatalf("log order wrong: %q / %q", last2[0].GetMessage(), last2[1].GetMessage())
	}
}

func TestWorkerMCP_GetCurrentIdle(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)

	sess := connectWorkerMCP(t)
	res := callTool(t, sess, "get_current_task", struct{}{})
	if !res.IsError {
		t.Fatalf("get_current_task should error when idle, got: %q", toolText(res))
	}
}

func TestWorkerMCP_FailRetries(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "worker-mcp-fail-" + shortID()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: name, MaxAttempts: 1, Payload: instructionsPayload(nil)})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	sess := connectWorkerMCP(t)

	// Claim until we get it.
	for i := 0; i < 20; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			t.Fatalf("claim_next_task: %q", toolText(res))
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := decodeStructured(res, &v); err != nil {
			t.Fatal(err)
		}
		if v.ID == id {
			break
		}
		_ = callTool(t, sess, "complete_task", struct{}{})
	}

	// Fail the task — since MaxAttempts=1, it goes terminal.
	fail := callTool(t, sess, "fail_task", map[string]any{"message": "boom via MCP"})
	if fail.IsError {
		t.Fatalf("fail_task: %q", toolText(fail))
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "failed" {
		t.Errorf("status=%s want failed", got.GetTask().GetStatus())
	}
	if !strings.Contains(got.GetTask().GetLastError(), "boom via MCP") {
		t.Errorf("last_error=%q want contains 'boom via MCP'", got.GetTask().GetLastError())
	}
}

// TestWorkerMCP_SetJiraURL covers the new set_jira_url tool: a worker
// holding a claim attaches a JIRA URL, and admin sees it on the task.
func TestWorkerMCP_SetJiraURL(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "worker-mcp-jira-" + shortID()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: name, Payload: instructionsPayload(nil)})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	sess := connectWorkerMCP(t)
	for i := 0; i < 20; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			t.Fatalf("claim_next_task: %q", toolText(res))
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := decodeStructured(res, &v); err != nil {
			t.Fatal(err)
		}
		if v.ID == id {
			break
		}
		_ = callTool(t, sess, "complete_task", struct{}{})
	}

	const url = "https://acme.atlassian.net/browse/E2E-9"
	jira := callTool(t, sess, "set_jira_url", map[string]any{"url": url})
	if jira.IsError {
		t.Fatalf("set_jira_url: %q", toolText(jira))
	}

	// Empty URL must be a tool error.
	bad := callTool(t, sess, "set_jira_url", map[string]any{"url": ""})
	if !bad.IsError {
		t.Fatalf("set_jira_url('') should be IsError=true")
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetJiraUrl() != url {
		t.Errorf("jira_url=%q want %q", got.GetTask().GetJiraUrl(), url)
	}

	// Cleanup.
	_ = callTool(t, sess, "complete_task", struct{}{})
}

// TestWorkerMCP_OpenPR covers the new open_pr tool: claim → progress
// (forces in_progress), open_pr; admin sees status=pr_opened, pr_url
// set, claim released. The worker is then idle so it can claim again.
func TestWorkerMCP_OpenPR(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := "worker-mcp-openpr-" + shortID()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: name, Payload: instructionsPayload(nil)})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	sess := connectWorkerMCP(t)
	for i := 0; i < 20; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			t.Fatalf("claim_next_task: %q", toolText(res))
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := decodeStructured(res, &v); err != nil {
			t.Fatal(err)
		}
		if v.ID == id {
			break
		}
		_ = callTool(t, sess, "complete_task", struct{}{})
	}

	// update_progress flips claimed → in_progress (the only state
	// open_pr accepts).
	_ = callTool(t, sess, "update_progress", map[string]any{"note": "PR ready"})

	// Empty URL is a tool error.
	bad := callTool(t, sess, "open_pr", map[string]any{"github_pr_url": ""})
	if !bad.IsError {
		t.Fatalf("open_pr('') should be IsError=true")
	}

	const prURL = "https://github.com/org/repo/pull/777"
	res := callTool(t, sess, "open_pr", map[string]any{"github_pr_url": prURL})
	if res.IsError {
		t.Fatalf("open_pr: %q", toolText(res))
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "pr_opened" {
		t.Errorf("status=%q want pr_opened", got.GetTask().GetStatus())
	}
	if got.GetTask().GetGithubPrUrl() != prURL {
		t.Errorf("github_pr_url=%q want %q", got.GetTask().GetGithubPrUrl(), prURL)
	}
	if got.GetTask().GetClaimedBy() != "" {
		t.Errorf("claim not released: claimed_by=%q", got.GetTask().GetClaimedBy())
	}

	// After open_pr the worker is idle: get_current_task must error.
	cur := callTool(t, sess, "get_current_task", struct{}{})
	if !cur.IsError {
		t.Errorf("after open_pr, get_current_task should be IsError=true: %q", toolText(cur))
	}

	// Cleanup: finalize via admin.
	if _, err := admin.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
		Id:      id,
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	}); err != nil {
		t.Fatalf("cleanup FinalizeTask: %v", err)
	}
}

// TestWorkerMCP_ClaimSurfacesInstructions covers the worker MCP TaskView
// shape: `instructions` is split out of payload so the agent sees it
// directly on the response of claim_next_task / get_current_task.
func TestWorkerMCP_ClaimSurfacesInstructions(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)
	admin := adminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "worker-mcp-instr-" + shortID()
	const text = "implement the worker MCP TaskView"
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name: name, Payload: instructionsPayload(map[string]any{"instructions": text}),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	sess := connectWorkerMCP(t)
	var seen string
	for i := 0; i < 20; i++ {
		res := callTool(t, sess, "claim_next_task", struct{}{})
		if res.IsError {
			t.Fatalf("claim_next_task: %q", toolText(res))
		}
		var v struct {
			ID           string `json:"id"`
			Instructions string `json:"instructions"`
		}
		if err := decodeStructured(res, &v); err != nil {
			t.Fatal(err)
		}
		if v.ID == id {
			seen = v.Instructions
			break
		}
		_ = callTool(t, sess, "complete_task", struct{}{})
	}
	if seen != text {
		t.Errorf("instructions=%q want %q", seen, text)
	}
	_ = callTool(t, sess, "complete_task", struct{}{})
}
