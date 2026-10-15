package serviceauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const token = "product-service-inbound-token-value"
const rotatedToken = "product-service-inbound-token-2nd"

// --- the set ---------------------------------------------------------------

func TestAcceptsOnlyConfiguredTokens(t *testing.T) {
	set := NewTokenSet([]string{token})

	if !set.Accepts(token) {
		t.Error("the configured token was rejected")
	}
	for _, bad := range []string{"", "nope", token + "x", token[:len(token)-1], strings.ToUpper(token)} {
		if set.Accepts(bad) {
			t.Errorf("token %q was accepted", bad)
		}
	}
}

func TestRotationIsSupported(t *testing.T) {
	set := NewTokenSet([]string{token, rotatedToken})
	for _, good := range []string{token, rotatedToken} {
		if !set.Accepts(good) {
			t.Errorf("token %q was rejected by a set that lists it", good)
		}
	}
}

// An empty set must refuse everything. It is the defence beneath the startup
// check, and a Server built directly in a test must not be accidentally open.
func TestAnEmptySetRefusesEverything(t *testing.T) {
	if NewTokenSet(nil).Accepts(token) {
		t.Error("an empty TokenSet accepted a token")
	}
}

func TestTheTokenIsNotStoredInPlaintext(t *testing.T) {
	set := NewTokenSet([]string{token})
	// Crude but real: it proves the raw value is not reachable through the
	// struct's rendered form, which is what a heap dump or a stray %v would show.
	if strings.Contains(fmt.Sprintf("%+v", set), token) {
		t.Error("the token appears in the rendered TokenSet; it must be held as a digest")
	}
}

// --- header parsing --------------------------------------------------------

func TestOnlyBearerSchemeIsAccepted(t *testing.T) {
	cases := map[string]string{
		"correct":   "Bearer " + token,
		"lowercase": "bearer " + token,
		"basic":     "Basic " + token,
		"bare":      token,
		"empty":     "Bearer ",
		"spaced":    "Bearer " + token + " extra",
		"absent":    "",
	}

	want := map[string]bool{
		"correct": true, "lowercase": false, "basic": false, "bare": false,
		"empty": false, "spaced": false, "absent": false,
	}

	for name, header := range cases {
		h := http.Header{}
		if header != "" {
			h.Set("Authorization", header)
		}
		got := NewTokenSet([]string{token}).Accepts(TokenFromHeader(h))
		if got != want[name] {
			t.Errorf("%s: accepted = %v, want %v (header %q)", name, got, want[name], header)
		}
	}
}

// --- HTTP ------------------------------------------------------------------

func TestHTTPRequiresACredentialAndReportsTheWriteDidNotHappen(t *testing.T) {
	var reached int
	handler := NewTokenSet([]string{token}).HTTP(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reached != 0 {
		t.Fatalf("the handler ran %d time(s) without a credential; "+
			"authentication must stop the request, not report on it afterwards", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHTTPAdmitsACredentialedCaller(t *testing.T) {
	var reached int
	handler := NewTokenSet([]string{token}).HTTP(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reached != 1 {
		t.Errorf("the handler ran %d time(s), want 1", reached)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// An orchestrator that gets 401 from a liveness probe restarts the container,
// and restarting the process answering the probe turns a partial outage into a
// total one. This is the test that stops that regression.
func TestHealthProbesStayOpen(t *testing.T) {
	exempt := []string{"/health", "/readyz", "/version"}

	for _, path := range exempt {
		var reached bool
		handler := NewTokenSet([]string{token}).HTTP(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }),
			exempt,
		)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if !reached {
			t.Errorf("%s was refused without a credential; the probe must stay open", path)
		}
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s answered 401, which would make an orchestrator restart a healthy container", path)
		}
	}
}

func TestAnUnlistedPathIsStillGuarded(t *testing.T) {
	handler := NewTokenSet([]string{token}).HTTP(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		[]string{"/health"},
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/products", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — an exempt probe path must not widen the guard", rec.Code)
	}
}

// --- gRPC ------------------------------------------------------------------

func TestGRPCRequiresACredential(t *testing.T) {
	var reached bool
	interceptor := NewTokenSet([]string{token}).Unary()

	// No credential in the context.
	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"},
		func(ctx context.Context, req any) (any, error) { return "handler", nil })

	if reached {
		t.Fatal("the handler ran without a credential")
	}
	if s, _ := status.FromError(err); s.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", s.Code())
	}
	if resp != nil {
		t.Errorf("a refused call returned %v", resp)
	}
}

// Unauthenticated rather than PermissionDenied: the caller has not identified
// itself, which is a different situation from being identified and refused, and
// gRPC clients treat the two differently.
func TestGRPCReportsUnauthenticatedNotPermissionDenied(t *testing.T) {
	interceptor := NewTokenSet([]string{token}).Unary()

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, req any) (any, error) { return nil, nil })

	if got := status.Code(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
	if got := status.Code(err); got == codes.PermissionDenied {
		t.Error("PermissionDenied would tell the caller it was identified and then refused")
	}
}

func TestGRPCAdmitsACredentialedCaller(t *testing.T) {
	interceptor := NewTokenSet([]string{token}).Unary()

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+token))

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, req any) (any, error) { return "handler", nil })

	if err != nil {
		t.Fatalf("a credentialed call was refused: %v", err)
	}
	if resp != "handler" {
		t.Errorf("response = %v, want the handler's own result", resp)
	}
}

func TestGRPCReadsTheSameHeaderHTTPDoes(t *testing.T) {
	// gRPC transports HTTP/2 headers as metadata, so the two paths must agree or
	// one transport is silently unguarded.
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+token))

	if !NewTokenSet([]string{token}).Accepts(TokenFromMetadata(ctx)) {
		t.Error("a gRPC-metadata credential was rejected")
	}

	empty := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	if NewTokenSet([]string{token}).Accepts(TokenFromMetadata(empty)) {
		t.Error("empty metadata produced a usable credential")
	}
}

// --- streaming -------------------------------------------------------------

func TestStreamRequiresACredential(t *testing.T) {
	var reached bool
	interceptor := NewTokenSet([]string{token}).Stream()

	err := interceptor(nil, &fakeStream{ctx: context.Background()}, &grpc.StreamServerInfo{},
		func(srv any, stream grpc.ServerStream) error { reached = true; return nil })

	if reached {
		t.Error("the stream handler ran without a credential")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", status.Code(err))
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }
