// Package grpcx is the shared gRPC transport kit for the strict, versioned
// inter-service contracts (catalog.v1). It owns dialing the remote service
// and the in-process (bufconn) transport, so every gateway consumer and the
// composition root tune transport in exactly one place and the monolith and
// split deployments cannot drift.
package grpcx

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// bufSize bounds the in-process listener's buffer. Catalog responses are
// order-of-magnitude smaller; the size is a traffic-burst headroom, not a
// correctness boundary (bufconn blocks once full, like a socket).
const bufSize = 1 << 20

// Dial returns a client connection to the contract surface at address.
// address is a standard gRPC target: a bare host:port works, and a DNS
// service like "dns:///gogo-catalog.gogo.svc.cluster.local:18082" resolves
// through cluster DNS.
//
// Transport security: credentials are insecure until the deployment
// hardens the wire (mTLS across the split services); the contract itself is
// already versioned and immune to the switch.
func Dial(address, serviceToken string) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(tenantOutgoing(), serviceTokenOutgoing(serviceToken)),
	)
}

// serviceTokenOutgoing authenticates this process as a service caller. The
// gateway shares one credential with its trusted product-platform services;
// service-to-service identity never comes from an end-user request.
func serviceTokenOutgoing(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// InProcess returns a client connection to services registered in this
// process, carried over an in-process bufconn transport — no sockets, no
// network, but a real gRPC pairing. register mounts the services on the
// server behind the connection; the returned close stops the server and
// frees the connection, so the composition root must call it on shutdown.
//
// This is the same-process pairing consumers use when the catalog module
// lives in-process, and Dial when it is a standalone service, without
// changing a line of client code.
func InProcess(register func(grpc.ServiceRegistrar)) (*grpc.ClientConn, func(), error) {
	listener := bufconn.Listen(bufSize)
	server := grpc.NewServer(grpc.UnaryInterceptor(tenantIncoming()))
	register(server)
	go func() {
		// Serve returns after Stop/GracefulStop; nothing to surface here —
		// the server's lifecycle is owned by the returned close.
		_ = server.Serve(listener)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(tenantOutgoing()),
	)
	if err != nil {
		server.Stop()
		return nil, nil, err
	}

	return conn, func() {
		server.Stop()
		_ = conn.Close()
	}, nil
}
