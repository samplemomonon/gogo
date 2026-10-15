package productconsumer

import (
	"context"
	"net"
	"reflect"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// Shared test plumbing for the kit: an in-process gRPC pairing (the same shape the
// in-process deployment uses) and the breaker/caller construction the tests need
// to observe.

const testBufSize = 1 << 20

// connFor registers srv on a bufconn listener and dials it. The server is stopped
// when the test finishes, so a test never leaks a goroutine.
func connFor(t *testing.T, srv any) grpc.ClientConnInterface {
	t.Helper()

	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer()
	// srv is always one of the fake*Server types, each of which implements a single
	// generated service registrar. A nil *upstream means "no server for this
	// upstream", which is how the client is built with only the services a test
	// exercises.
	//
	// The nil check has to see through the interface: a test passes a typed nil
	// (*fakeWriteServer)(nil), which is a non-nil any, and registering it would
	// panic inside the generated code when it calls through the embedded value.
	if !isNilServer(srv) {
		register(t, server, srv)
	}
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = conn.Close()
	})
	return conn
}

// isNilServer reports whether srv holds no server, seeing through the typed nil a
// test passes for an upstream it does not exercise.
func isNilServer(srv any) bool {
	if srv == nil {
		return true
	}
	v := reflect.ValueOf(srv)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func register(t *testing.T, server *grpc.Server, srv any) {
	t.Helper()
	switch s := srv.(type) {
	case *fakeWriteServer:
		productv1.RegisterProductServiceServer(server, s)
	case *fakeBrowseServer:
		productlistingv1.RegisterProductListingServiceServer(server, s)
	case *fakeSearchServer:
		productsearchv1.RegisterProductSearchServiceServer(server, s)
	default:
		t.Fatalf("no registrar for %T", srv)
	}
}

// newCaller builds a caller with a default breaker. The cooldown is the package
// default, which is long enough that a test driving failures into the open state
// cannot accidentally half-open mid-test.
func newCaller() *grpcconsumer.Caller {
	return grpcconsumer.New(breaker.NewDefault(), vocabulary)
}

// counter counts upstream calls so a test can assert the client did not dial a
// dependency it should have short-circuited.
type counter struct{ n atomic.Int64 }

func (c *counter) inc()        { c.n.Add(1) }
func (c *counter) load() int64 { return c.n.Load() }
