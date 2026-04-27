package store

import (
	"context"
	"testing"
)

func TestOpenPR_HappyPath(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = s.Heartbeat(ctx, "w1", claimed.ID) // claimed -> in_progress

	if err := s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	if got.Status != StatusPROpened {
		t.Errorf("status=%q, want pr_opened", got.Status)
	}
	if got.GithubPRURL == nil || *got.GithubPRURL != "https://github.com/o/r/pull/1" {
		t.Errorf("github_pr_url=%v, want https://github.com/o/r/pull/1", got.GithubPRURL)
	}
	if got.ClaimedBy != nil || got.ClaimedAt != nil || got.LastHeartbeatAt != nil {
		t.Errorf("claim fields not cleared: %+v", got)
	}
}

func TestOpenPR_RejectsFromClaimed(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1") // status=claimed, no heartbeat yet

	if err := s.OpenPR(ctx, "w1", claimed.ID, "https://github.com/o/r/pull/1"); err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner (claimed isn't a valid source for open_pr)", err)
	}
}

func TestOpenPR_RejectsNonOwner(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = s.Heartbeat(ctx, "w1", claimed.ID)

	if err := s.OpenPR(ctx, "w2", claimed.ID, "https://github.com/o/r/pull/1"); err != ErrNotOwner {
		t.Errorf("got %v, want ErrNotOwner", err)
	}
}

func TestOpenPR_EmptyURLReturnsError(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = s.Heartbeat(ctx, "w1", claimed.ID)

	if err := s.OpenPR(ctx, "w1", claimed.ID, ""); err != ErrEmptyPRURL {
		t.Errorf("got %v, want ErrEmptyPRURL", err)
	}
}
