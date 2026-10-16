package grpcx

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	"gogo/internal/shared/tenant"
)

// The tenant travels as call metadata, so these tests exercise the two
// interceptors in isolation: what the client puts on the wire, and what the
// server reads back off it.

func TestTenantOutgoingInterceptor(t *testing.T) {
	t.Parallel()

	t.Run("copies the context tenant onto the outgoing metadata", func(t *testing.T) {
		t.Parallel()

		var captured context.Context
		invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			captured = ctx
			return nil
		}

		ctx := tenant.WithID(context.Background(), "acme")
		if err := tenantOutgoing()(ctx, "/test.Echo/Echo", nil, nil, nil, invoker); err != nil {
			t.Fatalf("interceptor: %v", err)
		}

		md, ok := metadata.FromOutgoingContext(captured)
		if !ok {
			t.Fatal("the outgoing context carries no metadata")
		}
		if got := md.Get(tenant.Header); len(got) != 1 || got[0] != "acme" {
			t.Errorf("%s = %v, want [acme]", tenant.Header, got)
		}
	})

	t.Run("adds no metadata when the context has no tenant", func(t *testing.T) {
		t.Parallel()

		var captured context.Context
		invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			captured = ctx
			return nil
		}

		if err := tenantOutgoing()(context.Background(), "/test.Echo/Echo", nil, nil, nil, invoker); err != nil {
			t.Fatalf("interceptor: %v", err)
		}

		// Silent, not defaulted: the receiver decides what a missing tenant
		// means. An interceptor that invented "default" here would make
		// "no tenant" indistinguishable from "the default tenant" on the wire.
		if md, ok := metadata.FromOutgoingContext(captured); ok {
			if got := md.Get(tenant.Header); len(got) > 0 {
				t.Errorf("%s = %v, want none", tenant.Header, got)
			}
		}
	})
}

func TestTenantIncomingInterceptor(t *testing.T) {
	t.Parallel()

	handler := func(ctx context.Context, _ any) (any, error) {
		id, ok := tenant.FromContext(ctx)
		if !ok {
			return "absent", nil
		}
		return string(id), nil
	}

	call := func(t *testing.T, ctx context.Context) any {
		t.Helper()
		reply, err := tenantIncoming()(ctx, nil, nil, handler)
		if err != nil {
			t.Fatalf("interceptor: %v", err)
		}
		return reply
	}

	t.Run("puts the metadata tenant on the handler's context", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(tenant.Header, "acme"))
		if got := call(t, ctx); got != "acme" {
			t.Errorf("handler saw %v, want acme", got)
		}
	})

	t.Run("an empty tenant value means no tenant", func(t *testing.T) {
		t.Parallel()

		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(tenant.Header, ""))
		if got := call(t, ctx); got != "absent" {
			t.Errorf("handler saw %v, want the tenant to be absent", got)
		}
	})

	t.Run("a call without metadata runs with no tenant", func(t *testing.T) {
		t.Parallel()

		if got := call(t, context.Background()); got != "absent" {
			t.Errorf("handler saw %v, want the tenant to be absent", got)
		}
	})
}

// TestInProcessCarriesTheTenant is the end-to-end pairing check: a bufconn
// client and server built by InProcess, with a real unary call across them.
// It proves the two interceptors compose — the same property production relies
// on, and one that unit tests of each interceptor alone cannot show.
func TestInProcessCarriesTheTenant(t *testing.T) {
	t.Parallel()

	const serviceName = "grpcx.test.Echo"
	var seen string

	desc := grpc.ServiceDesc{
		ServiceName: serviceName,
		HandlerType: (*echoService)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Echo",
			// A hand-rolled MethodDesc handler has to invoke the server
			// interceptor itself — generated code does the same — or the
			// interceptor chain is silently bypassed and this test would
			// "fail" for a reason production never sees.
			Handler: func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := &emptypb.Empty{}
				if err := dec(in); err != nil {
					return nil, err
				}
				handler := func(ctx context.Context, _ any) (any, error) {
					id, _ := tenant.FromContext(ctx)
					seen = string(id)
					return &emptypb.Empty{}, nil
				}
				if interceptor == nil {
					return handler(ctx, in)
				}
				return interceptor(ctx, in, &grpc.UnaryServerInfo{FullMethod: "/" + serviceName + "/Echo"}, handler)
			},
		}},
	}

	conn, closeConn, err := InProcess(func(registrar grpc.ServiceRegistrar) {
		// RegisterService checks that the implementation satisfies
		// HandlerType; the method itself is never called because the
		// MethodDesc below installs its own handler.
		registrar.RegisterService(&desc, echoImpl{})
	})
	if err != nil {
		t.Fatalf("InProcess: %v", err)
	}
	defer closeConn()

	ctx := tenant.WithID(context.Background(), "acme")
	if err := conn.Invoke(ctx, "/"+serviceName+"/Echo", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if seen != "acme" {
		t.Errorf("server saw tenant %q, want acme", seen)
	}
}

// echoService exists only to satisfy grpc.ServiceDesc.HandlerType, which
// requires a pointer to an interface.
type echoService interface {
	Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

// echoImpl satisfies echoService so RegisterService accepts the descriptor.
type echoImpl struct{}

func (echoImpl) Echo(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
