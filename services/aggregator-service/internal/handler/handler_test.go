package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/config"
)

// The middleware tests. What matters here is not that each wrapper calls the next
// handler, but the properties that are easy to get wrong: which layer wins when
// several apply, what a client is told versus what the log records, and that a
// panic in any layer leaves the server able to answer the next request.

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() *config.Config {
	return &config.Config{
		AppPort:             8085,
		ReadHeaderTimeout:   5 * time.Second,
		ReadTimeout:         15 * time.Second,
		WriteTimeout:        30 * time.Second,
		IdleTimeout:         60 * time.Second,
		ShutdownTimeout:     30 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
		LogLevel:            "info",
		LogFormat:           "json",
		RequestTimeout:      10 * time.Second,
		DependencyTimeout:   3 * time.Second,
	}
}

func TestRequestBudgetCancelsTheContext(t *testing.T) {
	var observed error
	handler := RequestBudget(20*time.Millisecond, testLogger())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			observed = r.Context().Err()
		}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	// The deadline lives on the request context, so it reaches every downstream call
	// without each resolver knowing about it. That is what makes a fan-out bounded.
	if !errors.Is(observed, context.DeadlineExceeded) {
		t.Fatalf("context error %v, want DeadlineExceeded", observed)
	}
}

func TestRequestBudgetLeavesAFastRequestAlone(t *testing.T) {
	var deadlineSet bool
	handler := RequestBudget(time.Second, testLogger())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, deadlineSet = r.Context().Deadline()
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/query", nil))
	if !deadlineSet {
		t.Fatal("the handler should see a deadline")
	}
}

func TestZeroBudgetIsDisabled(t *testing.T) {
	var deadlineSet bool
	handler := RequestBudget(0, testLogger())(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, deadlineSet = r.Context().Deadline()
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/query", nil))
	// A zero budget must disable the check rather than cancel every request
	// instantly, which would take the service down rather than bound it.
	if deadlineSet {
		t.Fatal("a zero budget should install no deadline")
	}
}

func TestBodyLimitRejectsAnOversizedPayload(t *testing.T) {
	var readErr error
	handler := BodyLimit(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	body := strings.NewReader(strings.Repeat("x", 1024))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/query", body))

	if readErr == nil {
		t.Fatal("an oversized body should fail to read")
	}
	// Without a cap, a body of arbitrary size is buffered into memory by one request.
}

func TestBodyLimitPassesASmallPayloadThrough(t *testing.T) {
	var got string
	handler := BodyLimit(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(`{"query":"{__typename}"}`)))
	if !strings.Contains(got, "__typename") {
		t.Fatalf("body %q", got)
	}
}

func TestRecoverTurnsAPanicIntoFiveHundred(t *testing.T) {
	handler := Recover(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("resolver bug")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", recorder.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The body is a GraphQL error envelope, because a client that only knows how to
	// parse one shape should not have to special-case a panic.
	errorsList, ok := payload["errors"].([]any)
	if !ok || len(errorsList) == 0 {
		t.Fatalf("payload %v", payload)
	}
}

func TestRecoverKeepsThePanicDetailOutOfTheResponse(t *testing.T) {
	handler := Recover(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A panic value that would be dangerous to echo: an internal address.
		panic("dial tcp 10.0.0.4:9090: connect: connection refused")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	// The stack trace and the panic value go to the log. A stack names internal
	// packages and file paths, and the caller only needs to know the call failed.
	for _, leak := range []string{"10.0.0.4", "panic", "goroutine", ".go:"} {
		if strings.Contains(recorder.Body.String(), leak) {
			t.Fatalf("response leaked %q: %s", leak, recorder.Body.String())
		}
	}
}

func TestSecurityHeadersAreSet(t *testing.T) {
	handler := SecurityHeaders(func(string) bool { return true })(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestOriginPolicyIsEnforced(t *testing.T) {
	handler := SecurityHeaders(func(origin string) bool { return origin == "https://shop.example" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("the handler must not run for a rejected origin")
		}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/query", nil)
	request.Header.Set("Origin", "https://evil.example")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", recorder.Code)
	}
}

func TestMissingOriginIsAllowed(t *testing.T) {
	// A request with no Origin is not a browser request. Rejecting those would break
	// curl and every server-to-server caller.
	handler := SecurityHeaders(func(string) bool { return false })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
}

func TestLoggingReportsStatusAndSize(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/query", nil))

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record); err != nil {
		t.Fatalf("decode log line: %v (raw: %q)", err, buf.String())
	}

	if record["status"] != float64(http.StatusTeapot) {
		t.Errorf("status %v", record["status"])
	}
	if record["bytes"] != float64(5) {
		t.Errorf("bytes %v, want 5", record["bytes"])
	}
	if record["path"] != "/query" {
		t.Errorf("path %v", record["path"])
	}
}

func TestStatusRecorderKeepsTheFirstStatus(t *testing.T) {
	// A second WriteHeader is a handler bug. net/http ignores it, and reporting the
	// second one would log a status the client never received.
	recorder := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}

	recorder.WriteHeader(http.StatusOK)
	recorder.WriteHeader(http.StatusTeapot)

	if recorder.status != http.StatusOK {
		t.Fatalf("status %d, want the first one", recorder.status)
	}
}

func TestStatusRecorderInfersOKFromAWrite(t *testing.T) {
	// A handler that writes without setting a status implies 200, and the log should
	// say so rather than reporting zero.
	recorder := &statusRecorder{ResponseWriter: httptest.NewRecorder()}

	if _, err := recorder.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if recorder.status != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.status)
	}
	if recorder.bytes != 1 {
		t.Fatalf("bytes %d", recorder.bytes)
	}
}

func TestStatusRecorderUnwrapsForStreaming(t *testing.T) {
	// The WebSocket and SSE transports need to reach the underlying writer through the
	// logging middleware to hijack or flush.
	recorder := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if recorder.Unwrap() == nil {
		t.Fatal("Unwrap should expose the underlying writer")
	}
	recorder.Flush()
}

func TestLivenessTouchesNoDependency(t *testing.T) {
	// A liveness probe that called a dependency would have Kubernetes restart the pod
	// whenever that dependency blipped, turning a partial outage into a full one.
	server := &Server{
		ReadyCheck: func(context.Context) error { return errors.New("everything is down") },
		Config:     testConfig(),
		Log:        testLogger(),
	}
	handler := server.Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("liveness status %d, want 200 even with every dependency down", recorder.Code)
	}
}

func TestHealthzIsAnAliasForHealth(t *testing.T) {
	// The other services in this repo expose both, and orchestrators differ on which
	// they probe.
	server := &Server{Config: testConfig(), Log: testLogger()}
	handler := server.Handler()

	for _, path := range []string{"/health", "/healthz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s status %d", path, recorder.Code)
		}
	}
}

func TestReadinessFailsWhenADependencyIsDown(t *testing.T) {
	server := &Server{
		ReadyCheck: func(context.Context) error { return errors.New("category-service: unavailable") },
		Config:     testConfig(),
		Log:        testLogger(),
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	// Readiness differs from liveness on purpose: Kubernetes removes an instance from
	// the load balancer on a readiness failure but kills it on a liveness one. So a
	// dependency outage should drain traffic without destroying the pod.
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "category-service") {
		t.Errorf("the failure should name the dependency: %s", recorder.Body.String())
	}
}

func TestReadinessWithNoCheckPasses(t *testing.T) {
	server := &Server{Config: testConfig(), Log: testLogger()}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
}

func TestVersionEndpointReportsTheBuild(t *testing.T) {
	server := &Server{Config: testConfig(), Log: testLogger()}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/version", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Let an operator or a container health check confirm which build is running
	// without starting the server.
	if payload["version"] == "" || payload["version"] == nil {
		t.Fatalf("payload %v", payload)
	}
}

func TestUnknownPathIsAGraphQLShapedError(t *testing.T) {
	server := &Server{GraphQL: http.NotFoundHandler(), Config: testConfig(), Log: testLogger()}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d", recorder.Code)
	}
	// A client that only parses the GraphQL envelope should not have to special-case a
	// typo'd path.
	if !strings.Contains(recorder.Body.String(), `"errors"`) {
		t.Fatalf("body %s", recorder.Body.String())
	}
}

func TestMiddlewareOrderCatchesAPanicInTheInnerLayer(t *testing.T) {
	// Recover is outermost, so a panic in anything behind it is caught. If the order
	// were reversed, a panic in the budget or logging middleware would escape.
	server := &Server{
		GraphQL: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		}),
		Config: testConfig(),
		Log:    testLogger(),
	}
	handler := server.Handler()

	for i := range 3 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("request %d: status %d, want 500", i, recorder.Code)
		}
	}
}

func TestQueryRouteIsRegistered(t *testing.T) {
	var reached bool
	server := &Server{
		GraphQL: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}),
		Config: testConfig(),
		Log:    testLogger(),
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))

	if !reached {
		t.Fatal("POST /query did not reach the GraphQL handler")
	}
}

func TestPlaygroundIsNotRoutedWhenDisabled(t *testing.T) {
	server := &Server{
		GraphQL: http.NotFoundHandler(),
		Config:  testConfig(),
		Log:     testLogger(),
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/playground", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 when the playground is disabled", recorder.Code)
	}
}

func TestPlaygroundIsRoutedWhenEnabled(t *testing.T) {
	server := &Server{
		GraphQL:    http.NotFoundHandler(),
		Playground: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		Config:     testConfig(),
		Log:        testLogger(),
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/playground", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
}

func TestClassifyStatus(t *testing.T) {
	// Each sentinel maps to a status a proxy or a client library can act on. These
	// are only used for failures outside execution, where there is no body to carry
	// the error, so the status is the only signal a caller gets.
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"rejected", fmt.Errorf("%w: too complex", apperror.ErrRejected), http.StatusUnprocessableEntity, "REJECTED"},
		{"invalid", fmt.Errorf("%w: bad field", apperror.ErrInvalidInput), http.StatusBadRequest, "BAD_USER_INPUT"},
		{"not found", fmt.Errorf("%w: gone", apperror.ErrNotFound), http.StatusNotFound, "NOT_FOUND"},
		{"conflict", fmt.Errorf("%w: taken", apperror.ErrConflict), http.StatusConflict, "CONFLICT"},
		{"unavailable", fmt.Errorf("%w: down", apperror.ErrUnavailable), http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
		{"unknown", errors.New("something else"), http.StatusInternalServerError, "INTERNAL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyStatus(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Errorf("got %d %s, want %d %s", status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}
