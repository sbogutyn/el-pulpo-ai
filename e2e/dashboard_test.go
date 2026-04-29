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

// TestHTTP_DashboardRequiresAuth covers the dashboard family at the auth
// boundary: every protected route returns 401 without basic auth.
func TestHTTP_DashboardRequiresAuth(t *testing.T) {
	requireEndpointsReady(t)
	for _, path := range []string{
		"/dashboard",
		"/dashboard/fragment",
		"/wireframes",
	} {
		resp := httpRequest(t, http.MethodGet, path, nil, false)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without auth: status=%d want 401", path, resp.StatusCode)
		}
	}
}

// TestHTTP_DashboardOK covers the agent-centric dashboard page.
func TestHTTP_DashboardOK(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/dashboard")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	// HTMX poll on the dashboard fragment endpoint should be present.
	if !strings.Contains(body, "/dashboard/fragment") {
		t.Errorf("dashboard body missing /dashboard/fragment poll target: %q", body[:min(500, len(body))])
	}
}

// TestHTTP_DashboardFragment covers the HTMX poll endpoint: it returns
// markup but should not include a full <html> wrapper.
func TestHTTP_DashboardFragment(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/dashboard/fragment")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if strings.Contains(strings.ToLower(body), "<html") {
		t.Errorf("fragment should not include <html>; body=%q", body[:min(500, len(body))])
	}
}

// TestHTTP_DashboardAgentDetail covers the agent-detail subpage. Drive
// a worker through one task so the agent identity exists, then GET the
// detail page for that worker id and assert it renders with the id
// embedded.
func TestHTTP_DashboardAgentDetail(t *testing.T) {
	requireEndpointsReady(t)
	worker := workerClient(t)
	admin := adminClient(t)

	wid := "e2e-agent-" + shortID()
	id := claimWithName(t, worker, admin, wid, "agent-detail-"+shortID())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// One log line so the agent panel has something to show.
	if _, err := worker.AppendLog(ctx, &pb.AppendLogRequest{
		WorkerId: wid, TaskId: id, Message: "hello from agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatal(err)
	}

	resp := httpGetAuth(t, "/dashboard/agents/"+wid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, wid) {
		t.Errorf("agent detail page missing worker id %q; body=%q", wid, body[:min(500, len(body))])
	}
}

// TestHTTP_Wireframes covers the protected /wireframes redirect into
// the static bundle.
func TestHTTP_Wireframes(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/wireframes")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/static/wireframes/" {
		t.Errorf("Location=%q want /static/wireframes/", loc)
	}

	// Follow the redirect manually: the static bundle index must serve OK.
	resp = httpGetAuth(t, "/static/wireframes/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static wireframes status=%d want 200", resp.StatusCode)
	}
}
