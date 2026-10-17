package grpcx

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"gogo/internal/shared/authn"
	"gogo/internal/shared/tenant"
)

// The tenant travels to a remote service as call metadata, not as a field on
// every request message. It is a property of the caller's context (which
// website's Host the request arrived on), not of the product being asked for,
// so encoding it in the contract would duplicate the boundary on every message
// and invite a caller to contradict it.
//
// Both interceptors are silent when the metadata is absent. A call that never
// had a tenant therefore stays a call with no tenant, and the receiver's
// FromContextOrDefault decides it — the same rule the HTTP gateway applies to
// a request with no matching host. Nothing here can invent a tenant.

// ServerOptions are the server-side options a contract service must install so
// an inbound call carries its tenant on the context. A standalone deployment
// builds its gRPC server with these:
//
//	grpc.NewServer(grpcx.ServerOptions()...)
//
// Without them the handlers still run — they fall back to the default tenant —
// which is why this is an option list rather than a constructor: the split
// services compose their own server, and the tenant is added the same way in
// every one of them.
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.UnaryInterceptor(tenantIncoming())}
}

// tenantOutgoing copies the call's tenant onto the outgoing metadata, so a
// remote service acts for the same website the local request was resolved to.
// It is a client unary interceptor: Dial and InProcess install it, so no
// consumer has to remember to thread the tenant.
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
		if id := authn.Session(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, authn.SessionHeader, id)
		}
		if caller := authn.Caller(ctx); caller != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, authn.CallerHeader, caller)
		}
		if credential := authn.ServiceCredential(ctx); credential != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, authn.CallerCredentialHeader, credential)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// tenantIncoming reads the tenant off the incoming metadata and puts it on the
// server-side context, so a gRPC handler resolves the tenant exactly the way an
// HTTP handler does. A server with no tenant in the metadata is not an error:
// the caller's context simply carried none.
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
		if values := md.Get(authn.SessionHeader); len(values) > 0 && values[0] != "" {
			ctx = authn.WithSession(ctx, values[0])
		}
		if values := md.Get(authn.CallerHeader); len(values) > 0 && values[0] != "" {
			ctx = authn.WithCaller(ctx, values[0])
		}
		if values := md.Get(authn.CallerCredentialHeader); len(values) > 0 && values[0] != "" {
			ctx = authn.WithCallerCredential(ctx, values[0])
		}
		return handler(ctx, req)
	}
}
