package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

func authedReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetBasicAuth("u", "p")
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

func TestListTasks_Unauthenticated(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d", rr.Code)
	}
}

func TestCreateAndListTask(t *testing.T) {
	srv := newServer(t)

	form := url.Values{
		"name":         {"hello"},
		"priority":     {"5"},
		"max_attempts": {"3"},
		"payload":      {`{"a":1}`},
	}.Encode()

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusSeeOther && rr.Code != http.StatusOK {
		t.Errorf("create code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list code=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hello") {
		t.Errorf("list missing task: %s", rr.Body.String())
	}
}

func TestCreateTask_InvalidJSON(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"name":    {"x"},
		"payload": {"not-json"},
	}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d, want 400", rr.Code)
	}
}

func TestCreateTask_WithIssueRefs(t *testing.T) {
	t.Skip("enabled in Task 12 once list template renders short forms")
	srv := newServer(t)

	form := url.Values{
		"name":          {"with-refs"},
		"priority":      {"0"},
		"max_attempts":  {"3"},
		"payload":       {"{}"},
		"jira_url":      {"https://acme.atlassian.net/browse/PROJ-1"},
		"github_pr_url": {"https://github.com/acme/widget/pull/7"},
	}.Encode()

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create code=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PROJ-1") {
		t.Errorf("list missing JIRA short form: %s", body)
	}
	if !strings.Contains(body, "acme/widget#7") {
		t.Errorf("list missing PR short form: %s", body)
	}
}

func TestCreateTask_InvalidJiraURL(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"name":     {"x"},
		"payload":  {"{}"},
		"jira_url": {"not-a-jira-url"},
	}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "JIRA") {
		t.Errorf("response missing JIRA error hint: %s", rr.Body.String())
	}
}

func TestCreateTask_InvalidPRURL(t *testing.T) {
	srv := newServer(t)
	form := url.Values{
		"name":          {"x"},
		"payload":       {"{}"},
		"github_pr_url": {"https://github.com/x/y/issues/1"},
	}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
}

func TestDeleteTask(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	task, err := s.CreateTask(context.Background(), store.NewTaskInput{Name: "kill-me", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/delete", ""))
	if rr.Code != http.StatusSeeOther {
		t.Errorf("code=%d", rr.Code)
	}
}

func TestUpdateLinks_WorksOnFailedTask(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task, err := s.CreateTask(context.Background(), store.NewTaskInput{Name: "x", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Force the task into "failed".
	if _, err := s.Pool().Exec(context.Background(), `UPDATE tasks SET status='failed' WHERE id=$1`, task.ID); err != nil {
		t.Fatalf("force status: %v", err)
	}

	form := url.Values{
		"jira_url":      {"https://acme.atlassian.net/browse/PROJ-9"},
		"github_pr_url": {"https://github.com/acme/widget/pull/42"},
	}.Encode()

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/links", form))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.JiraURL == nil || *got.JiraURL != "https://acme.atlassian.net/browse/PROJ-9" {
		t.Errorf("JiraURL not saved: %v", got.JiraURL)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != "https://github.com/acme/widget/pull/42" {
		t.Errorf("GithubPRURL not saved: %v", got.GithubPRURL)
	}
}

func TestUpdateLinks_InvalidJira(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	task, _ := s.CreateTask(context.Background(), store.NewTaskInput{Name: "x", MaxAttempts: 3})

	form := url.Values{"jira_url": {"nope"}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/links", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400", rr.Code)
	}
}
