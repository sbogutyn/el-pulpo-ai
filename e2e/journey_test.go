//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// TestJourney_EndToEnd is the integration journey: admin creates a task,
// worker (driven over MCP) progresses and logs it, admin UI sees the
// progress, worker completes it, admin + metrics confirm.
//
// This is the single most important test in the suite: it proves that
// mastermind gRPC, the admin UI, the worker MCP endpoint, and the
// Prometheus metrics all agree about one task's lifecycle end-to-end.
func TestJourney_EndToEnd(t *testing.T) {
	requireEndpointsReady(t)
	workerMCP_drain(t)

	admin := adminClient(t)
	mcpSess := connectWorkerMCP(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. admin creates the task via gRPC.
	name := "journey-" + shortID()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
		Name:     name,
		Payload:  []byte(`{"step":"journey"}`),
		Priority: 100, // high priority so it's claimed next
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	// 2. baseline metrics for completed counter.
	beforeCompleted := scrapeCounter(t, "tasks_completed_total")

	// 3. worker (MCP) claims; retry past any stray items.
	for i := 0; i < 20; i++ {
		res := callTool(t, mcpSess, "claim_next_task", struct{}{})
		if res.IsError {
			t.Fatalf("claim_next_task: %q", toolText(res))
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := decodeStructured(res, &v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if v.ID == id {
			break
		}
		_ = callTool(t, mcpSess, "complete_task", struct{}{})
	}

	// 4. progress + logs.
	_ = callTool(t, mcpSess, "update_progress", map[string]any{"note": "starting"})
	_ = callTool(t, mcpSess, "append_log", map[string]any{"message": "journey step 1"})
	_ = callTool(t, mcpSess, "append_log", map[string]any{"message": "journey step 2"})

	// 5. admin UI shows the progress note.
	resp := httpGetAuth(t, "/tasks/"+id)
	if resp.StatusCode != http.StatusOK {
		failWithLogs(t, "admin detail status=%d", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, "starting") {
		lim := len(body)
		if lim > 600 {
			lim = 600
		}
		t.Errorf("admin detail body missing progress note 'starting'; body=%q", body[:lim])
	}

	// 6. complete.
	done := callTool(t, mcpSess, "complete_task", struct{}{})
	if done.IsError {
		t.Fatalf("complete_task: %q", toolText(done))
	}

	// 7. admin confirms status=completed.
	err = eventually(contextWithDeadline(ctx, 5*time.Second), 100*time.Millisecond, func() error {
		got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
		if err != nil {
			return err
		}
		if got.GetTask().GetStatus() != "completed" {
			return fmt.Errorf("status=%s want completed", got.GetTask().GetStatus())
		}
		return nil
	})
	if err != nil {
		failWithLogs(t, "task never became completed: %v", err)
	}

	// 8. admin confirms the log lines landed in order.
	logs, err := admin.ListTaskLogs(ctx, &pb.ListTaskLogsRequest{Id: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.GetItems()) < 2 {
		t.Fatalf("expected at least 2 log lines, got %d", len(logs.GetItems()))
	}
	last2 := logs.GetItems()[len(logs.GetItems())-2:]
	if !strings.Contains(last2[0].GetMessage(), "journey step 1") ||
		!strings.Contains(last2[1].GetMessage(), "journey step 2") {
		t.Fatalf("logs out of order: %q / %q", last2[0].GetMessage(), last2[1].GetMessage())
	}

	// 9. /metrics shows tasks_completed_total incremented by >= 1.
	afterCompleted := scrapeCounter(t, "tasks_completed_total")
	if afterCompleted <= beforeCompleted {
		t.Errorf("tasks_completed_total did not increment: before=%f after=%f", beforeCompleted, afterCompleted)
	}
}

// scrapeCounter reads `/metrics` and returns the numeric value of the
// metric line whose name equals `name`. Only metrics without labels are
// summed — sufficient for the counters the journey asserts on.
func scrapeCounter(t *testing.T, name string) float64 {
	t.Helper()
	resp := httpRequest(t, http.MethodGet, "/metrics", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status=%d", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<18))
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, name+" ") && !strings.HasPrefix(line, name+"{") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		return v
	}
	return 0
}
