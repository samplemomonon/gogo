package mediaconsumer

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mediav1 "gogo/contracts/media/v1"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// Shared test plumbing for the kit: an in-process gRPC pairing (the same shape a
// same-process deployment uses) and the breaker/caller construction the tests
// need to observe.

const testBufSize = 1 << 20

// connFor registers srv on a bufconn listener and dials it. The server is
// stopped when the test finishes, so a test never leaks a goroutine.
func connFor(t *testing.T, srv mediav1.MediaServiceServer) grpc.ClientConnInterface {
	t.Helper()

	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer()
	mediav1.RegisterMediaServiceServer(server, srv)
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

// newClient wires a Client over a fake server and a fresh default breaker. The
// cooldown is the package default, long enough that a test driving failures into
// the open state cannot accidentally half-open mid-test.
func newClient(t *testing.T, srv mediav1.MediaServiceServer) (*Client, *fakeMediaServer) {
	t.Helper()
	fake, ok := srv.(*fakeMediaServer)
	if !ok {
		t.Fatalf("newClient expects a *fakeMediaServer, got %T", srv)
	}
	return &Client{
		client: mediav1.NewMediaServiceClient(connFor(t, srv)),
		caller: grpcconsumer.New(breaker.NewDefault(), vocabulary),
	}, fake
}

// counter counts upstream calls so a test can assert the client did not dial a
// dependency it should have short-circuited.
type counter struct{ n atomic.Int64 }

func (c *counter) inc()        { c.n.Add(1) }
func (c *counter) load() int64 { return c.n.Load() }
