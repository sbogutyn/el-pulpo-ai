//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func TestProbes_Healthz(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpRequest(t, http.MethodGet, "/healthz", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1024))
	if !strings.Contains(body, "ok") {
		t.Errorf("body=%q should contain 'ok'", body)
	}
}

func TestProbes_Readyz(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpRequest(t, http.MethodGet, "/readyz", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
}

func TestProbes_Metrics(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpRequest(t, http.MethodGet, "/metrics", nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))

	// These are emitted unconditionally at startup: gauges and
	// unlabelled counters publish their zero values.
	want := []string{
		"tasks_completed_total",
		"tasks_reaped_total",
		"tasks_pending",
		"claim_duration_seconds",
	}
	for _, m := range want {
		if !strings.Contains(body, m) {
			t.Errorf("metric %q missing from /metrics", m)
		}
	}

	// tasks_claimed_total and tasks_failed_total are CounterVecs; they
	// only appear in /metrics once a label combination has been observed.
	// We verify those label-specific lines emerge after triggering the
	// relevant conditions: one empty claim observes
	// tasks_claimed_total{result="empty"}, and the subsequent test
	// TestWorkerGRPC_ReportTerminal observes tasks_failed_total. Here
	// we only assert the bare behaviour we can rely on during this test
	// run.
	worker := workerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A ClaimTask with an empty queue guarantees tasks_claimed_total
	// gains at least the "empty" label.
	for i := 0; i < 5; i++ {
		_, _ = worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "e2e-probe-drain"})
	}
	resp2 := httpRequest(t, http.MethodGet, "/metrics", nil, false)
	body2 := string(readBodyLimited(resp2.Body, 1<<16))
	if !strings.Contains(body2, "tasks_claimed_total") {
		t.Errorf("after empty claims, tasks_claimed_total still missing from /metrics")
	}
}

func TestProbes_WorkerHealthz(t *testing.T) {
	requireEndpointsReady(t)
	s := requireStack(t)
	resp, err := http.Get(s.WorkerMCPURL + "/healthz")
	if err != nil {
		t.Fatalf("worker /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
}
