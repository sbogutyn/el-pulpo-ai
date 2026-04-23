package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateAndGetTask(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	payload := json.RawMessage(`{"k":"v"}`)
	created, err := s.CreateTask(ctx, NewTaskInput{
		Name:         "my-task",
		Payload:      payload,
		Priority:     5,
		MaxAttempts:  4,
		ScheduledFor: nil,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("ID not set")
	}
	if created.Status != StatusPending {
		t.Errorf("status: got %q want pending", created.Status)
	}

	got, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Name != "my-task" || got.Priority != 5 || got.MaxAttempts != 4 {
		t.Errorf("mismatch: %+v", got)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, err := s.GetTask(ctx, uuid.New())
	if err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestListTasks_FilterAndPaginate(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	for i := 0; i < 5; i++ {
		if _, err := s.CreateTask(ctx, NewTaskInput{Name: "t", Payload: []byte("{}"), MaxAttempts: 3}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	page, err := s.ListTasks(ctx, ListTasksFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("len=%d, want 2", len(page.Items))
	}
	if page.Total != 5 {
		t.Errorf("total=%d, want 5", page.Total)
	}
}

func TestCreateAndGetTask_WithIssueRefs(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	jira := "https://acme.atlassian.net/browse/PROJ-1"
	pr := "https://github.com/acme/widget/pull/7"

	created, err := s.CreateTask(ctx, NewTaskInput{
		Name:        "with-refs",
		Payload:     json.RawMessage(`{}`),
		MaxAttempts: 3,
		JiraURL:     &jira,
		GithubPRURL: &pr,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.JiraURL == nil || *created.JiraURL != jira {
		t.Errorf("JiraURL: got %v, want %q", created.JiraURL, jira)
	}
	if created.GithubPRURL == nil || *created.GithubPRURL != pr {
		t.Errorf("GithubPRURL: got %v, want %q", created.GithubPRURL, pr)
	}

	got, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.JiraURL == nil || *got.JiraURL != jira {
		t.Errorf("GetTask JiraURL mismatch: %v", got.JiraURL)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != pr {
		t.Errorf("GetTask GithubPRURL mismatch: %v", got.GithubPRURL)
	}
}

func TestCreateTask_NoIssueRefs_StoresNull(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, err := s.CreateTask(ctx, NewTaskInput{
		Name: "no-refs", Payload: json.RawMessage(`{}`), MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.JiraURL != nil || created.GithubPRURL != nil {
		t.Errorf("expected nil refs, got jira=%v pr=%v", created.JiraURL, created.GithubPRURL)
	}
}
