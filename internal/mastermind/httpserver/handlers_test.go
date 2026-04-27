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
	body := rr.Body.String()
	if !strings.Contains(body, `role="alert"`) {
		t.Errorf("response missing error banner: %s", body)
	}
	if !strings.Contains(body, "JIRA URL must look like") {
		t.Errorf("response missing JIRA validation message: %s", body)
	}
}

func TestUpdateLinks_EmptyFormClearsRefs(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	jira := "https://acme.atlassian.net/browse/PROJ-1"
	pr := "https://github.com/acme/widget/pull/2"
	task, err := s.CreateTask(context.Background(), store.NewTaskInput{
		Name: "clear-refs", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	form := url.Values{"jira_url": {""}, "github_pr_url": {""}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/links", form))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.JiraURL != nil {
		t.Errorf("JiraURL not cleared: %q", *got.JiraURL)
	}
	if got.GithubPRURL != nil {
		t.Errorf("GithubPRURL not cleared: %q", *got.GithubPRURL)
	}
}

func TestDetailPage_ShowsRefsAndUpdateForm(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()

	jira := "https://acme.atlassian.net/browse/PROJ-5"
	pr := "https://github.com/acme/widget/pull/11"
	task, _ := s.CreateTask(context.Background(), store.NewTaskInput{
		Name: "detail", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})
	// Force a non-pending status to prove the update form is still shown.
	if _, err := s.Pool().Exec(context.Background(), `UPDATE tasks SET status='failed' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks/"+task.ID.String(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PROJ-5") {
		t.Errorf("detail missing JIRA short form: %s", body)
	}
	if !strings.Contains(body, "acme/widget#11") {
		t.Errorf("detail missing PR short form: %s", body)
	}
	if !strings.Contains(body, `action="/tasks/`+task.ID.String()+`/links"`) {
		t.Errorf("detail missing link-update form: %s", body)
	}
}

func TestDetailPage_ShowsProgressNote(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()

	task, _ := s.CreateTask(context.Background(), store.NewTaskInput{Name: "detail", MaxAttempts: 3})
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE tasks SET status='in_progress', claimed_by='w1', progress_note='step 2/3' WHERE id=$1`,
		task.ID,
	); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks/"+task.ID.String(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Progress:") {
		t.Errorf("detail missing Progress row: %s", body)
	}
	if !strings.Contains(body, "step 2/3") {
		t.Errorf("detail missing progress_note value: %s", body)
	}
}

// prOpenedTask creates a task and drives it to pr_opened via raw SQL,
// mirroring what OpenPR does atomically.
func prOpenedTask(t *testing.T, s *store.Store) store.Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), store.NewTaskInput{Name: "parked", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE tasks SET status='pr_opened', github_pr_url='https://github.com/o/r/pull/1',
		 claimed_by=NULL, claimed_at=NULL, last_heartbeat_at=NULL WHERE id=$1`,
		task.ID,
	); err != nil {
		t.Fatalf("force pr_opened: %v", err)
	}
	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return got
}

func TestTasksRequestReview_RedirectsOnSuccess(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task := prOpenedTask(t, s)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/request-review", ""))
	if rr.Code != http.StatusSeeOther {
		t.Errorf("code=%d, want 303", rr.Code)
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.StatusReviewRequested {
		t.Errorf("status=%q, want review_requested", got.Status)
	}
}

func TestTasksRequestReview_NotFound(t *testing.T) {
	srv := newServer(t)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/00000000-0000-0000-0000-000000000000/request-review", ""))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code=%d, want 404", rr.Code)
	}
}

func TestTasksRequestReview_RejectsFromInProgress(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task, err := s.CreateTask(context.Background(), store.NewTaskInput{Name: "in-progress", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE tasks SET status='in_progress', claimed_by='w1' WHERE id=$1`, task.ID,
	); err != nil {
		t.Fatalf("force in_progress: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/request-review", ""))
	if rr.Code != http.StatusConflict {
		t.Errorf("code=%d, want 409", rr.Code)
	}
}

func TestTasksFinalize_Success(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task := prOpenedTask(t, s)

	form := url.Values{"outcome": {"success"}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/finalize", form))
	if rr.Code != http.StatusSeeOther {
		t.Errorf("code=%d, want 303", rr.Code)
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.StatusCompleted {
		t.Errorf("status=%q, want completed", got.Status)
	}
}

func TestTasksFinalize_Failure(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task := prOpenedTask(t, s)

	form := url.Values{"outcome": {"failure"}, "message": {"bad PR"}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/finalize", form))
	if rr.Code != http.StatusSeeOther {
		t.Errorf("code=%d, want 303", rr.Code)
	}

	got, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("status=%q, want failed", got.Status)
	}
	if got.LastError == nil || *got.LastError != "bad PR" {
		t.Errorf("last_error=%v, want 'bad PR'", got.LastError)
	}
}

func TestTasksFinalize_RejectsBadOutcome(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	task := prOpenedTask(t, s)

	form := url.Values{"outcome": {"bogus"}}.Encode()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodPost, "/tasks/"+task.ID.String()+"/finalize", form))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code=%d, want 400", rr.Code)
	}
}

func TestTasksDetail_RendersInstructions(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	payload := `{"instructions":"do the thing","other":"ignored"}`
	task, err := s.CreateTask(context.Background(), store.NewTaskInput{
		Name:        "with-instructions",
		MaxAttempts: 3,
		Payload:     []byte(payload),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/tasks/"+task.ID.String(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "do the thing") {
		t.Errorf("detail missing instructions text: %s", body)
	}
	if !strings.Contains(body, "task-instructions") {
		t.Errorf("detail missing instructions section class: %s", body)
	}
}
