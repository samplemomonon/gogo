package grpcsrv

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryRecovery turns a panicking unary handler into an Internal error.
//
// This is not a nicety. A panic inside a gRPC handler takes the whole process
// down, not just the request: grpc-go runs each RPC on its own goroutine and does
// not recover, so an unrecovered panic exits the service and every in-flight call
// on every other connection dies with it. net/http is the opposite — a panicking
// handler is contained to its goroutine and the server keeps serving.
//
// The asymmetry matters for review. A panic in an HTTP handler here is an incident;
// the same bug in a gRPC handler is an outage, reachable by any caller that can
// reach the port.
//
// The panic value and the stack go to the log, never to the caller. A stack trace
// names internal packages and file paths, and the caller only needs to know the
// call failed.
func UnaryRecovery(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			log.Error("panic in grpc handler",
				"error", r,
				"method", info.FullMethod,
				"stack", string(debug.Stack()),
			)
			// The named return is what makes this work: without assigning to err,
			// the recovered panic would return a nil error and a nil response,
			// which gRPC turns into an empty success.
			err = status.Error(codes.Internal, "internal server error")
		}()
		return handler(ctx, req)
	}
}

// StreamRecovery is UnaryRecovery for streaming handlers.
//
// It is a separate function because a stream handler has a different signature,
// and installing only the unary interceptor would leave every streaming RPC
// unprotected — the protection is exactly as partial as the wiring.
func StreamRecovery(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			log.Error("panic in grpc stream handler",
				"error", r,
				"method", info.FullMethod,
				"stack", string(debug.Stack()),
			)
			err = status.Error(codes.Internal, "internal server error")
		}()
		return handler(srv, ss)
	}
}
