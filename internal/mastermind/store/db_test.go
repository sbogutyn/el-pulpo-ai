package store

import (
	"context"
	"testing"
)

func TestOpen_PingsSuccessfully(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testDSN)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
