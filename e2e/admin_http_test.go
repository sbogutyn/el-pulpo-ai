//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func TestHTTP_RootRedirect(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpRequest(t, http.MethodGet, "/", nil, false)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/tasks" {
		t.Fatalf("Location=%q want /tasks", loc)
	}
}

func TestHTTP_TasksRequiresAuth(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpRequest(t, http.MethodGet, "/tasks", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestHTTP_TasksListOK(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/tasks")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, "<table") {
		t.Errorf("expected a table in /tasks body, got: %q", body[:min(300, len(body))])
	}
}

func TestHTTP_TasksFragment(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/tasks/fragment")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if strings.Contains(body, "<html") {
		t.Errorf("fragment should not include <html>, got: %q", body[:min(300, len(body))])
	}
	if !strings.Contains(body, "<tr") && !strings.Contains(body, "<tbody") {
		t.Errorf("fragment missing table body: %q", body[:min(300, len(body))])
	}
}

func TestHTTP_NewForm(t *testing.T) {
	requireEndpointsReady(t)
	resp := httpGetAuth(t, "/tasks/new")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, `name="name"`) {
		t.Errorf("new form missing name input: %q", body[:min(500, len(body))])
	}
}

// TestHTTP_CreateTask and TestHTTP_TaskDetail run in sequence because they
// share state — but each test isolates via a unique task name.
func TestHTTP_CreateTask(t *testing.T) {
	requireEndpointsReady(t)
	name := "http-create-" + shortID()
	form := url.Values{
		"name":        {name},
		"priority":    {"5"},
		"max_attempts": {"2"},
		"payload":     {`{"source":"http-test"}`},
	}
	resp := httpRequest(t, http.MethodPost, "/tasks", form, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (body=%s)", resp.StatusCode, readBodyLimited(resp.Body, 1024))
	}

	// Verify via AdminService that the task exists.
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if countByName(list.GetItems(), name) != 1 {
		t.Fatalf("created task %q not found", name)
	}
}

func TestHTTP_TaskDetail(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create via admin gRPC so this test is independent of the create
	// HTTP test.
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "http-detail-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	resp := httpGetAuth(t, "/tasks/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, id) {
		t.Errorf("detail page missing id %s; body=%q", id, body[:min(500, len(body))])
	}
}

func TestHTTP_EditForm(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "http-edit-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	resp := httpGetAuth(t, "/tasks/"+id+"/edit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body := string(readBodyLimited(resp.Body, 1<<16))
	if !strings.Contains(body, `name="name"`) {
		t.Errorf("edit form missing name input: %q", body[:min(300, len(body))])
	}
}

func TestHTTP_UpdateTask(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "http-update-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	newName := "http-updated-" + shortID()
	form := url.Values{
		"name":        {newName},
		"priority":    {"7"},
		"max_attempts": {"5"},
		"payload":     {"{}"},
	}
	resp := httpRequest(t, http.MethodPost, "/tasks/"+id, form, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (body=%s)", resp.StatusCode, readBodyLimited(resp.Body, 1024))
	}
	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetName() != newName {
		t.Errorf("name=%q want %q", got.GetTask().GetName(), newName)
	}
	if got.GetTask().GetPriority() != 7 {
		t.Errorf("priority=%d want 7", got.GetTask().GetPriority())
	}
}

func TestHTTP_UpdateLinks(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "http-links-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	form := url.Values{
		"jira_url":      {"https://acme.atlassian.net/browse/PROJ-999"},
		"github_pr_url": {"https://github.com/org/repo/pull/42"},
	}
	resp := httpRequest(t, http.MethodPost, "/tasks/"+id+"/links", form, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (body=%s)", resp.StatusCode, readBodyLimited(resp.Body, 1024))
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetJiraUrl() != "https://acme.atlassian.net/browse/PROJ-999" {
		t.Errorf("jira_url=%q", got.GetTask().GetJiraUrl())
	}
	if got.GetTask().GetGithubPrUrl() != "https://github.com/org/repo/pull/42" {
		t.Errorf("github_pr_url=%q", got.GetTask().GetGithubPrUrl())
	}
}

func TestHTTP_Requeue(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	worker := workerClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Prepare a completed task by running it through worker gRPC.
	wid := "e2e-http-requeue-" + shortID()
	name := "http-requeue-" + shortID()
	id := claimWithName(t, worker, admin, wid, name)
	if _, err := worker.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: wid, TaskId: id,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatal(err)
	}

	resp := httpRequest(t, http.MethodPost, "/tasks/"+id+"/requeue", url.Values{}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (body=%s)", resp.StatusCode, readBodyLimited(resp.Body, 1024))
	}

	got, err := admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTask().GetStatus() != "pending" {
		t.Errorf("status=%s want pending", got.GetTask().GetStatus())
	}

	// Drain the requeued task so the next test starts clean.
	for i := 0; i < 10; i++ {
		r, err := worker.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: wid})
		if err != nil {
			break
		}
		_, _ = worker.ReportResult(ctx, &pb.ReportResultRequest{
			WorkerId: wid, TaskId: r.GetTask().GetId(),
			Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
		})
		if r.GetTask().GetId() == id {
			break
		}
	}
}

func TestHTTP_Delete(t *testing.T) {
	requireEndpointsReady(t)
	admin := adminClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{Name: "http-delete-" + shortID()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetTask().GetId()

	resp := httpRequest(t, http.MethodPost, "/tasks/"+id+"/delete", url.Values{}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d want 303 (body=%s)", resp.StatusCode, readBodyLimited(resp.Body, 1024))
	}

	_, err = admin.GetTask(ctx, &pb.GetTaskRequest{Id: id})
	if err == nil {
		t.Fatal("expected NotFound after delete, got task")
	}
}

func TestHTTP_StaticAuth(t *testing.T) {
	requireEndpointsReady(t)
	// Without auth → 401.
	resp := httpRequest(t, http.MethodGet, "/static/htmx.min.js", nil, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth status=%d want 401", resp.StatusCode)
	}
	// With auth → 200 and non-empty body.
	resp = httpGetAuth(t, "/static/htmx.min.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status=%d want 200", resp.StatusCode)
	}
	body := readBodyLimited(resp.Body, 1<<14)
	if len(body) < 100 {
		t.Errorf("htmx.min.js body suspiciously short (%d bytes)", len(body))
	}
}

