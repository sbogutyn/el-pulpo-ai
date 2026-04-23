package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUpdateTask_OnlyWhilePending(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "x", MaxAttempts: 3})

	sched := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	upd, err := s.UpdateTask(ctx, created.ID, UpdateTaskInput{
		Name: "y", Priority: 7, MaxAttempts: 5, ScheduledFor: &sched,
		Payload: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if upd.Name != "y" || upd.Priority != 7 || upd.MaxAttempts != 5 {
		t.Errorf("update mismatch: %+v", upd)
	}

	// Force to completed, then update should error.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='completed' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, created.ID, UpdateTaskInput{Name: "z"}); err != ErrNotEditable {
		t.Errorf("want ErrNotEditable, got %v", err)
	}
}

func TestDeleteTask_NotAllowedWhileActive(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "x", MaxAttempts: 3})
	// Simulate a claim.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='claimed', claimed_by='w', claimed_at=now(), last_heartbeat_at=now() WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, created.ID); err != ErrNotDeletable {
		t.Errorf("want ErrNotDeletable, got %v", err)
	}

	// Once completed, delete should succeed.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='completed' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, created.ID); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestRequeueTask(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "x", MaxAttempts: 3})
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='failed', last_error='boom', attempt_count=3 WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}

	reset, err := s.RequeueTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("RequeueTask: %v", err)
	}
	if reset.Status != StatusPending {
		t.Errorf("status=%q, want pending", reset.Status)
	}
	if reset.AttemptCount != 0 {
		t.Errorf("attempts=%d, want 0", reset.AttemptCount)
	}
	if reset.LastError != nil {
		t.Errorf("last_error not cleared: %v", *reset.LastError)
	}

	// Requeue a pending task is a no-op but still succeeds.
	if _, err := s.RequeueTask(ctx, created.ID); err != nil {
		t.Errorf("requeue pending: %v", err)
	}

	// Requeue while active should be rejected.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequeueTask(ctx, created.ID); err != ErrNotRequeueable {
		t.Errorf("want ErrNotRequeueable, got %v", err)
	}
}

func TestUpdateTaskLinks_WorksInAnyStatus(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "x", MaxAttempts: 3})

	// Force a status where UpdateTask would fail.
	if _, err := s.pool.Exec(ctx, `UPDATE tasks SET status='failed' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}

	jira := "https://acme.atlassian.net/browse/PROJ-9"
	pr := "https://github.com/acme/widget/pull/42"
	upd, err := s.UpdateTaskLinks(ctx, created.ID, &jira, &pr)
	if err != nil {
		t.Fatalf("UpdateTaskLinks: %v", err)
	}
	if upd.JiraURL == nil || *upd.JiraURL != jira {
		t.Errorf("JiraURL mismatch: %v", upd.JiraURL)
	}
	if upd.GithubPRURL == nil || *upd.GithubPRURL != pr {
		t.Errorf("GithubPRURL mismatch: %v", upd.GithubPRURL)
	}
	if upd.Status != "failed" {
		t.Errorf("status changed: %q", upd.Status)
	}
}

func TestUpdateTaskLinks_NilClears(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	jira := "https://acme.atlassian.net/browse/PROJ-9"
	pr := "https://github.com/acme/widget/pull/42"
	created, _ := s.CreateTask(ctx, NewTaskInput{
		Name: "x", MaxAttempts: 3, JiraURL: &jira, GithubPRURL: &pr,
	})

	upd, err := s.UpdateTaskLinks(ctx, created.ID, nil, nil)
	if err != nil {
		t.Fatalf("UpdateTaskLinks: %v", err)
	}
	if upd.JiraURL != nil {
		t.Errorf("JiraURL not cleared: %v", *upd.JiraURL)
	}
	if upd.GithubPRURL != nil {
		t.Errorf("GithubPRURL not cleared: %v", *upd.GithubPRURL)
	}
}

func TestUpdateTaskLinks_NotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, err := s.UpdateTaskLinks(ctx, uuid.New(), nil, nil)
	if err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
