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
