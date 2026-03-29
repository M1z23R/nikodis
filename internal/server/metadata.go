package server

import (
	"context"

	"google.golang.org/grpc/metadata"
)

const (
	namespaceHeader = "x-nikodis-namespace"
	tokenHeader     = "x-nikodis-token"
	defaultNS       = "default"
)

func extractNamespace(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return defaultNS
	}
	vals := md.Get(namespaceHeader)
	if len(vals) == 0 || vals[0] == "" {
		return defaultNS
	}
	return vals[0]
}

func extractToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(tokenHeader)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func namespacedKey(ns, key string) string {
	return ns + ":" + key
}
