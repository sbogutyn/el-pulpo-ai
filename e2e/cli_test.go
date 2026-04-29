//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// runElpulpo invokes the host-built `elpulpo` binary against the e2e
// stack. Returns combined stdout+stderr (CLI prints results to stdout
// and errors to stderr; either may carry the signal under test).
func runElpulpo(t *testing.T, args ...string) (stdout string, stderr string, err error) {
	t.Helper()
	if S == nil || S.ElpulpoBin == "" {
		t.Skip("elpulpo binary not built; skipping CLI tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, S.ElpulpoBin, args...)
	cmd.Env = append([]string{},
		"PATH="+os.Getenv("PATH"),
		"MASTERMIND_ADDR="+S.MastermindGRPC,
		"ADMIN_TOKEN="+S.AdminToken,
		"REQUEST_TIMEOUT=10s",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// TestCLI_TasksCreateInstructions covers `elpulpo tasks create
// --instructions ...`: the CLI must splice instructions into payload so
// the gRPC server's mandatory-instructions validator passes.
func TestCLI_TasksCreateInstructions(t *testing.T) {
	requireEndpointsReady(t)
	if S.ElpulpoBin == "" {
		t.Skip("elpulpo not built")
	}

	name := "cli-create-" + shortID()
	out, errOut, err := runElpulpo(t,
		"tasks", "create",
		"--name", name,
		"--instructions", "do the cli test",
		"--json",
	)
	if err != nil {
		t.Fatalf("elpulpo tasks create: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	var resp struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", jerr, out)
	}
	if resp.Name != name {
		t.Errorf("name=%q want %q", resp.Name, name)
	}
	if resp.Status != "pending" {
		t.Errorf("status=%q want pending", resp.Status)
	}

	// Round-trip through the admin gRPC: the task we just created must
	// have payload.instructions == "do the cli test".
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: resp.ID})
	if err != nil {
		t.Fatal(err)
	}
	var pl struct {
		Instructions string `json:"instructions"`
	}
	if jerr := json.Unmarshal(got.GetTask().GetPayload(), &pl); jerr != nil {
		t.Fatalf("payload not JSON: %v", jerr)
	}
	if pl.Instructions != "do the cli test" {
		t.Errorf("payload.instructions=%q want %q", pl.Instructions, "do the cli test")
	}
}

// TestCLI_TasksRequestReviewAndFinalize drives a parked task through the
// admin pipeline using the elpulpo CLI: parks via worker gRPC, calls
// `tasks request-review` then `tasks finalize --success`, and verifies
// the final status.
func TestCLI_TasksRequestReviewAndFinalize(t *testing.T) {
	requireEndpointsReady(t)
	if S.ElpulpoBin == "" {
		t.Skip("elpulpo not built")
	}

	id, _ := parkTask(t, "https://github.com/org/repo/pull/600")

	out, errOut, err := runElpulpo(t, "tasks", "request-review", id, "--json")
	if err != nil {
		t.Fatalf("request-review: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	var rr struct {
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(out), &rr); jerr != nil {
		t.Fatalf("decode request-review: %v\nstdout=%s", jerr, out)
	}
	if rr.Status != "review_requested" {
		t.Errorf("status=%q want review_requested", rr.Status)
	}

	out, errOut, err = runElpulpo(t, "tasks", "finalize", id, "--success", "--json")
	if err != nil {
		t.Fatalf("finalize: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	var fin struct {
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(out), &fin); jerr != nil {
		t.Fatalf("decode finalize: %v\nstdout=%s", jerr, out)
	}
	if fin.Status != "completed" {
		t.Errorf("status=%q want completed", fin.Status)
	}
}

// TestCLI_TasksFinalizeFailure covers `--fail "reason"`: the message
// must propagate to last_error on the task.
func TestCLI_TasksFinalizeFailure(t *testing.T) {
	requireEndpointsReady(t)
	if S.ElpulpoBin == "" {
		t.Skip("elpulpo not built")
	}

	id, _ := parkTask(t, "https://github.com/org/repo/pull/601")

	const reason = "rejected via cli"
	if _, _, err := runElpulpo(t, "tasks", "finalize", id, "--fail", reason); err != nil {
		t.Fatalf("finalize --fail: %v", err)
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

// TestCLI_TasksListIncludesParkedStates covers the `--status` filter
// taking pr_opened and review_requested, which were added to the
// help-text catalog when the rename landed.
func TestCLI_TasksListIncludesParkedStates(t *testing.T) {
	requireEndpointsReady(t)
	if S.ElpulpoBin == "" {
		t.Skip("elpulpo not built")
	}

	id, _ := parkTask(t, "https://github.com/org/repo/pull/602")

	out, errOut, err := runElpulpo(t, "tasks", "list", "--status", "pr_opened", "--limit", "500", "--json")
	if err != nil {
		t.Fatalf("list pr_opened: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, id) {
		t.Errorf("listing of pr_opened tasks did not include %s; out=%q", id, out[:min(len(out), 800)])
	}

	// Cleanup.
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := admin.FinalizeTask(ctx, &pb.FinalizeTaskRequest{
		Id:      id,
		Outcome: &pb.FinalizeTaskRequest_Success_{Success: &pb.FinalizeTaskRequest_Success{}},
	}); err != nil {
		t.Fatalf("cleanup FinalizeTask: %v", err)
	}
}
