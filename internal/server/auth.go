package server

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dimitrije/nikodis/internal/config"
)

func newAuthInterceptor(cfg *config.Config) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := authenticate(ctx, cfg); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func newStreamAuthInterceptor(cfg *config.Config) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := authenticate(ss.Context(), cfg); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func authenticate(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return nil // no config = auth disabled
	}
	ns := extractNamespace(ctx)
	expected, ok := cfg.Namespaces[ns]
	if !ok {
		return status.Errorf(codes.Unauthenticated, "unknown namespace %q", ns)
	}
	if expected == "" {
		return nil // empty token = no auth for this namespace
	}
	token := extractToken(ctx)
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}
