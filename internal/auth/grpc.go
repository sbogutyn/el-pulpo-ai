// Package auth provides gRPC bearer-token and HTTP basic-auth middlewares.
package auth

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// BearerInterceptor returns a unary server interceptor that validates the
// "authorization: Bearer <token>" metadata against the expected token using
// a constant-time comparison.
func BearerInterceptor(expected string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		got := strings.TrimPrefix(vals[0], "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(ctx, req)
	}
}

// PerMethodInterceptor returns a unary server interceptor that validates the
// "authorization: Bearer <token>" metadata against a per-method expected token.
// Methods not present in policy receive codes.Unimplemented — this is a
// deliberate fail-closed default so forgetting to wire a method up cannot
// leak an unauthenticated call path.
func PerMethodInterceptor(policy map[string]string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		expected, ok := policy[info.FullMethod]
		if !ok {
			return nil, status.Errorf(codes.Unimplemented, "method %s has no auth policy", info.FullMethod)
		}
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		got := strings.TrimPrefix(vals[0], "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid token")
		}
		return handler(ctx, req)
	}
}

// BearerCredentials returns the per-RPC credentials a client should attach
// to outgoing calls so the server's BearerInterceptor accepts them.
func BearerCredentials(token string) BearerToken { return BearerToken(token) }

type BearerToken string

func (t BearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}

func (BearerToken) RequireTransportSecurity() bool { return false }
