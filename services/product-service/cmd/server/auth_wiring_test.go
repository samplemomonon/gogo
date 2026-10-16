package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"product-service/gen/productpb"
	"product-service/internal/config"
	"product-service/internal/serviceauth"
)

// The serviceauth package's own tests prove the mechanism. These prove the
// service installs it — the half that disappears silently when someone edits
// main.go, and which no amount of unit testing inside serviceauth would catch.
//
// Verified by sabotage: removing either guard from main.go left the whole suite
// green before guardedHandler and grpcServerOptions were extracted.

const inboundToken = "product-service-inbound-token-value"

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newSet(t *testing.T) *serviceauth.TokenSet {
	t.Helper()
	return serviceauth.NewTokenSet([]string{inboundToken})
}

// metadataWithToken returns a client context carrying the credential.
//
// Outgoing, not incoming: this is the side that sends. The server reads it back
// as incoming metadata, which is what serviceauth.TokenFromMetadata reads — the
// same header, arriving through the transport rather than set by hand.
func metadataWithToken(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx,
		metadata.Pairs("authorization", "Bearer "+inboundToken))
}

// recordingProductServer answers writes so the test can tell "refused by the
// guard" from "reached the handler and failed for another reason".
type recordingProductServer struct {
	productpb.UnimplementedProductServiceServer

	reached atomic.Int64
}

func (s *recordingProductServer) CreateProduct(context.Context, *productpb.CreateProductRequest) (*productpb.CreateProductResponse, error) {
	s.reached.Add(1)
	return &productpb.CreateProductResponse{Product: &productpb.Product{Name: "created"}}, nil
}

// --- HTTP wiring -----------------------------------------------------------

// A refused request must not reach the handler. The counter is the point: an
// authentication control that reports on a request after letting it through is
// not a control.
func TestGuardedHandlerStopsAnUnauthenticatedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/products", func(http.ResponseWriter, *http.Request) { reached++ })

	handler := guardedHandler(mux, []string{inboundToken})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/products", nil))

	if reached != 0 {
		t.Fatalf("the write handler ran %d time(s) without a credential", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGuardedHandlerAdmitsACredentialedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/products", func(http.ResponseWriter, *http.Request) { reached++ })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", nil)
	req.Header.Set("Authorization", "Bearer "+inboundToken)

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(rec, req)

	if reached != 1 {
		t.Errorf("the write handler ran %d time(s), want 1", reached)
	}
}

// The regression that would restart healthy containers. probePaths is the list
// production passes, so this test reads the same value main.go does rather than
// restating it.
func TestProbesStayOpenThroughTheRealGuard(t *testing.T) {
	for _, path := range probePaths {
		var reached bool
		mux := http.NewServeMux()
		mux.HandleFunc("GET "+path, func(http.ResponseWriter, *http.Request) { reached = true })

		rec := httptest.NewRecorder()
		guardedHandler(mux, []string{inboundToken}).ServeHTTP(
			rec, httptest.NewRequest(http.MethodGet, path, nil))

		if !reached {
			t.Errorf("%s was refused through the production guard", path)
		}
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s answered 401; an orchestrator would restart a healthy container", path)
		}
	}
}

// An exempt probe path must not widen the guard for anything else.
func TestAnExemptProbeDoesNotOpenTheAPISurface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/products", func(http.ResponseWriter, *http.Request) {})

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodPost, "/api/v1/products", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// --- gRPC wiring -----------------------------------------------------------

// A real server, started with exactly the options production passes, reached
// over an in-memory listener. bufconn rather than a live port: the transport is
// not what is under test, and binding a port in a unit test is a flake waiting
// to happen.
func startGRPC(t *testing.T, opts []grpc.ServerOption, impl productpb.ProductServiceServer) productpb.ProductServiceClient {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(opts...)
	productpb.RegisterProductServiceServer(server, impl)

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return productpb.NewProductServiceClient(conn)
}

func TestGRPCServerRefusesAnUnauthenticatedWrite(t *testing.T) {
	server := &recordingProductServer{}
	client := startGRPC(t, grpcServerOptions(newSet(t), testLog()), server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.CreateProduct(ctx, &productpb.CreateProductRequest{Name: "X"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", status.Code(err))
	}
	if got := server.reached.Load(); got != 0 {
		t.Fatalf("the write handler ran %d time(s) without a credential", got)
	}
}

func TestGRPCServerAdmitsACredentialedWrite(t *testing.T) {
	client := startGRPC(t, grpcServerOptions(newSet(t), testLog()), &recordingProductServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadataWithToken(ctx)

	if _, err := client.CreateProduct(ctx, &productpb.CreateProductRequest{Name: "X"}); err != nil {
		t.Errorf("a credentialed call was refused: %v", err)
	}
}

// gRPC health is deliberately NOT covered here.
//
// It would need the health server registration extracted out of buildGRPCServer
// alongside the options, and it is answered by gRPC before interceptors run, so
// the interceptor chain cannot affect it. The HTTP probe test above covers the
// regression that matters — a probe answering 401 restarts a healthy container —
// and adding a guard that covered gRPC health would be a separate change to
// check separately.

// The whole chain, through the function production actually calls.
//
// The tests above cover guardedHandler and grpcServerOptions. This one covers the
// call site that joins them to the mux — the last seam where a guard can be
// removed without any test noticing.
func TestTheWholeChainRefusesAnUnauthenticatedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/products", func(http.ResponseWriter, *http.Request) { reached++ })

	handler := wrapHandler(mux, &config.Config{InboundServiceTokens: []string{inboundToken}}, testLog())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/products", nil))

	if reached != 0 {
		t.Fatalf("the write handler ran %d time(s) through the production chain", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTheWholeChainStillServesProbes(t *testing.T) {
	var reached bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(http.ResponseWriter, *http.Request) { reached = true })

	handler := wrapHandler(mux, &config.Config{InboundServiceTokens: []string{inboundToken}}, testLog())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if !reached {
		t.Error("/health was refused through the production chain")
	}
	if rec.Code == http.StatusUnauthorized {
		t.Error("/health answered 401 through the production chain")
	}
}
