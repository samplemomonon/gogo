package grpcsrv

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"category-service/gen/categorypb"
)

// panicHandler is the bug this guards against: a handler that panics.
func panicHandler(context.Context, any) (any, error) {
	panic("something went badly wrong")
}

func TestUnaryRecoveryTurnsAPanicIntoAnInternalError(t *testing.T) {
	t.Parallel()

	interceptor := UnaryRecovery(slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := &grpc.UnaryServerInfo{FullMethod: "/category.v1.CategoryService/GetCategory"}

	// The test fails here if the panic escapes: an unrecovered panic in a gRPC
	// handler is what takes the process down in production.
	resp, err := interceptor(context.Background(), nil, info, panicHandler)

	if resp != nil {
		t.Errorf("resp = %v, want nil so the caller cannot read a half-built message", resp)
	}
	if err == nil {
		t.Fatal("err = nil, want an Internal error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// The panic value must not reach the caller: it names internal detail, and a
// client has no use for a stack trace.
func TestUnaryRecoveryDoesNotLeakThePanicValue(t *testing.T) {
	t.Parallel()

	interceptor := UnaryRecovery(slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := &grpc.UnaryServerInfo{FullMethod: "/category.v1.CategoryService/GetCategory"}

	_, err := interceptor(context.Background(), nil, info, panicHandler)

	if strings.Contains(err.Error(), "something went badly wrong") {
		t.Errorf("err = %q, want the panic value kept out of the response", err)
	}
}

// The stack has to reach the log, or the recovery makes panics undiagnosable.
func TestUnaryRecoveryLogsTheStackTrace(t *testing.T) {
	t.Parallel()

	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	interceptor := UnaryRecovery(log)
	info := &grpc.UnaryServerInfo{FullMethod: "/category.v1.CategoryService/GetCategory"}

	if _, err := interceptor(context.Background(), nil, info, panicHandler); err == nil {
		t.Fatal("err = nil, want the panic recovered")
	}

	out := logged.String()
	if !strings.Contains(out, "something went badly wrong") {
		t.Errorf("log does not carry the panic value:\n%s", out)
	}
	if !strings.Contains(out, "goroutine") && !strings.Contains(out, "recovery.go") {
		t.Errorf("log does not carry a stack trace:\n%s", out)
	}
	if !strings.Contains(out, "/category.v1.CategoryService/GetCategory") {
		t.Errorf("log does not name the method that panicked:\n%s", out)
	}
}

// The interceptor must be transparent for handlers that do not panic, including
// their error values, or every successful call would start reporting Internal.
func TestUnaryRecoveryIsTransparentOnTheHappyPath(t *testing.T) {
	t.Parallel()

	interceptor := UnaryRecovery(slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := &grpc.UnaryServerInfo{FullMethod: "/category.v1.CategoryService/GetCategory"}

	want := &categorypb.GetCategoryResponse{}
	got, err := interceptor(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return want, nil })

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != any(want) {
		t.Errorf("resp = %v, want the handler's response passed through", got)
	}
}

func TestStreamRecoveryTurnsAPanicIntoAnInternalError(t *testing.T) {
	t.Parallel()

	interceptor := StreamRecovery(slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := &grpc.StreamServerInfo{FullMethod: "/listing.v1.ListingService/WatchProducts"}

	err := interceptor(nil, nil, info, func(any, grpc.ServerStream) error {
		panic("stream handler exploded")
	})

	if err == nil {
		t.Fatal("err = nil, want an Internal error")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code = %v, want Internal", got)
	}
	if strings.Contains(err.Error(), "stream handler exploded") {
		t.Errorf("err = %q, want the panic value kept out of the response", err)
	}
}
