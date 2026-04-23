package auth

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestBearerInterceptor_MissingMetadata(t *testing.T) {
	h := BearerInterceptor("expected-token")
	_, err := h(context.Background(), nil, nil, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code=%v, want Unauthenticated", status.Code(err))
	}
}

func TestBearerInterceptor_WrongToken(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer wrong"))
	h := BearerInterceptor("expected-token")
	_, err := h(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code=%v, want Unauthenticated", status.Code(err))
	}
}

func TestBearerInterceptor_Allows(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer expected-token"))
	h := BearerInterceptor("expected-token")
	resp, err := h(ctx, "req", nil, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp != "ok" {
		t.Errorf("handler not called")
	}
}
