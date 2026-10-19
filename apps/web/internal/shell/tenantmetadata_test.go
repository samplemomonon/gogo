package shell_test

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"gogo/internal/shared/tenant"
)

// The tenant metadata chain, test-side.
//
// These interceptors mirror the production pair in shared/grpcx so the platform fakes
// see exactly what a real service sees from a gateway call. They are duplicated rather
// than exported because grpcx keeps them unexported by design: no consumer should be
// reaching for the tenant interceptor, since Dial installs it. Only a test standing up
// its own fake server needs to.
//
// Between them they prove the chain end to end: a request arriving on a Host resolves to
// a tenant in the HTTP middleware, grpcx stamps it on every outbound gRPC call, and the
// receiving service reads it back off its context. A break anywhere in that chain shows
// up in these tests as one tenant being served another's rows.

// tenantOutgoing stamps the resolved tenant on an outgoing call.
func tenantOutgoing() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if id, ok := tenant.FromContext(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, tenant.Header, string(id))
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// tenantIncoming reads the tenant off the incoming metadata onto the server-side
// context, so a handler resolves the tenant the way a real service's would.
func tenantIncoming() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}
		if values := md.Get(tenant.Header); len(values) > 0 && values[0] != "" {
			ctx = tenant.WithID(ctx, tenant.ID(values[0]))
		}
		return handler(ctx, req)
	}
}

// tenantFromIncoming reads the tenant header off a server-side context.
func tenantFromIncoming(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if values := md.Get(tenant.Header); len(values) > 0 {
		return values[0]
	}
	return ""
}
