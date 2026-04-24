package auth

import (
	"context"
	"testing"

	"google.golang.org/grpc"
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

func TestPerMethodInterceptor_RoutesByMethod(t *testing.T) {
	policy := map[string]string{
		"/pkg.Svc/Worker": "w-tok",
		"/pkg.Svc/Admin":  "a-tok",
	}
	itc := PerMethodInterceptor(policy)

	call := func(method, tok string) error {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+tok))
		_, err := itc(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(ctx context.Context, req any) (any, error) { return "ok", nil })
		return err
	}

	if err := call("/pkg.Svc/Worker", "w-tok"); err != nil {
		t.Errorf("worker happy path: %v", err)
	}
	if err := call("/pkg.Svc/Admin", "a-tok"); err != nil {
		t.Errorf("admin happy path: %v", err)
	}
	if err := call("/pkg.Svc/Worker", "a-tok"); status.Code(err) != codes.Unauthenticated {
		t.Errorf("worker method with admin token: code=%v want Unauthenticated", status.Code(err))
	}
	if err := call("/pkg.Svc/Admin", "w-tok"); status.Code(err) != codes.Unauthenticated {
		t.Errorf("admin method with worker token: code=%v want Unauthenticated", status.Code(err))
	}
	if err := call("/pkg.Svc/Unknown", "w-tok"); status.Code(err) != codes.Unimplemented {
		t.Errorf("unknown method: code=%v want Unimplemented", status.Code(err))
	}

	// Missing metadata entirely.
	_, err := itc(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Worker"},
		func(ctx context.Context, req any) (any, error) { return "ok", nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing metadata: code=%v want Unauthenticated", status.Code(err))
	}
}
