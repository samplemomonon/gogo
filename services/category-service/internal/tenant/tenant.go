// Package tenant carries the resolved merchant scope through category-service.
package tenant

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const MetadataKey = "x-tenant-id"

type contextKey struct{}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, strings.TrimSpace(id))
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

func IncomingUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(MetadataKey); len(values) > 0 {
				ctx = WithID(ctx, values[0])
			}
		}
		return handler(ctx, req)
	}
}
