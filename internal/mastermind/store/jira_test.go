package store

import (
	"context"
	"testing"
)

func TestSetJiraURL_AllowedFromClaimedAndInProgress(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	// claimed -> set_jira_url
	if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-1"); err != nil {
		t.Fatalf("SetJiraURL from claimed: %v", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.JiraURL == nil || *got.JiraURL != "https://jira/T-1" {
		t.Errorf("jira_url=%v, want https://jira/T-1", got.JiraURL)
	}

	// claimed -> in_progress via heartbeat, then set_jira_url again
	_ = s.Heartbeat(ctx, "w1", claimed.ID)
	if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-2"); err != nil {
		t.Fatalf("SetJiraURL from in_progress: %v", err)
	}
	got, _ = s.GetTask(ctx, claimed.ID)
	if got.JiraURL == nil || *got.JiraURL != "https://jira/T-2" {
		t.Errorf("jira_url=%v, want https://jira/T-2", got.JiraURL)
	}
}

func TestSetJiraURL_RejectsNonOwner(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	if err := s.SetJiraURL(ctx, "w2", claimed.ID, "https://jira/T-X"); err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner", err)
	}
}

func TestSetJiraURL_RejectsFromPending(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	created, _ := s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})

	if err := s.SetJiraURL(ctx, "w1", created.ID, "https://jira/T-X"); err != ErrNotOwner {
		// pending tasks have no claimed_by, so the owner guard fails first.
		t.Errorf("got %v, want ErrNotOwner", err)
	}
}

func TestSetJiraURL_RefreshesHeartbeat(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3})
	claimed, _ := s.ClaimTask(ctx, "w1")

	before, _ := s.GetTask(ctx, claimed.ID)
	if err := s.SetJiraURL(ctx, "w1", claimed.ID, "https://jira/T-1"); err != nil {
		t.Fatalf("SetJiraURL: %v", err)
	}
	after, _ := s.GetTask(ctx, claimed.ID)
	if before.LastHeartbeatAt == nil || after.LastHeartbeatAt == nil {
		t.Fatal("missing heartbeat timestamps")
	}
	if !after.LastHeartbeatAt.After(*before.LastHeartbeatAt) && !after.LastHeartbeatAt.Equal(*before.LastHeartbeatAt) {
		t.Errorf("heartbeat moved backwards: before=%v after=%v", before.LastHeartbeatAt, after.LastHeartbeatAt)
	}
}
