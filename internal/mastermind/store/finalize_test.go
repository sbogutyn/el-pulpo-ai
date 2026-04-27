package store

import (
	"context"
	"testing"
)

// openedPR is a small helper used by the finalize and requeue tests below
// to drive a freshly-created task all the way to pr_opened.
func openedPR(t *testing.T, s *Store, ctx context.Context, worker string) Task {
	t.Helper()
	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, worker)
	_ = s.Heartbeat(ctx, worker, claimed.ID)
	if err := s.OpenPR(ctx, worker, claimed.ID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	got, _ := s.GetTask(ctx, claimed.ID)
	return got
}

func TestRequestReview_HappyPath(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	parked := openedPR(t, s, ctx, "w1")

	if err := s.RequestReview(ctx, parked.ID); err != nil {
		t.Fatalf("RequestReview: %v", err)
	}
	got, _ := s.GetTask(ctx, parked.ID)
	if got.Status != StatusReviewRequested {
		t.Errorf("status=%q, want review_requested", got.Status)
	}
}

func TestRequestReview_RejectsFromInProgress(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = s.Heartbeat(ctx, "w1", claimed.ID)

	if err := s.RequestReview(ctx, claimed.ID); err != ErrInvalidTransition {
		t.Errorf("got %v, want ErrInvalidTransition", err)
	}
}

func TestFinalizeTask_SuccessFromPROpened(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	parked := openedPR(t, s, ctx, "w1")

	if err := s.FinalizeTask(ctx, parked.ID, true, ""); err != nil {
		t.Fatalf("FinalizeTask: %v", err)
	}
	got, _ := s.GetTask(ctx, parked.ID)
	if got.Status != StatusCompleted {
		t.Errorf("status=%q, want completed", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at not set")
	}
	if got.GithubPRURL == nil {
		t.Error("github_pr_url should be preserved on success")
	}
}

func TestFinalizeTask_FailureFromReviewRequested(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	parked := openedPR(t, s, ctx, "w1")
	_ = s.RequestReview(ctx, parked.ID)

	if err := s.FinalizeTask(ctx, parked.ID, false, "rejected"); err != nil {
		t.Fatalf("FinalizeTask: %v", err)
	}
	got, _ := s.GetTask(ctx, parked.ID)
	if got.Status != StatusFailed {
		t.Errorf("status=%q, want failed", got.Status)
	}
	if got.LastError == nil || *got.LastError != "rejected" {
		t.Errorf("last_error=%v, want rejected", got.LastError)
	}
}

func TestFinalizeTask_RejectsFromInProgress(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(ctx, testDSN)
	defer s.Close()
	truncate(t, s.pool)

	_, _ = s.CreateTask(ctx, NewTaskInput{Name: "t", MaxAttempts: 3, Payload: []byte(`{"instructions":"test"}`)})
	claimed, _ := s.ClaimTask(ctx, "w1")
	_ = s.Heartbeat(ctx, "w1", claimed.ID)

	if err := s.FinalizeTask(ctx, claimed.ID, true, ""); err != ErrInvalidTransition {
		t.Errorf("got %v, want ErrInvalidTransition", err)
	}
}
