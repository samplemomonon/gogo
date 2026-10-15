package grpcclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"aggregator-service/gen/categorypb"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/retry"
)

// The resilience tests live with the transport rather than with the breaker and retry
// packages, because the property under test spans all three: one downstream operation
// goes through the breaker, then the retry loop, then a per-attempt deadline. Each of
// the other two packages tests its own behaviour in isolation, and this file tests that
// they compose into the order the design requires.

// flakyCategoryServer fails a fixed number of times before succeeding.
//
// It embeds the existing double and overrides only the one method under test, so the
// rest of the contract stays stubbed and an accidental call elsewhere still fails.
type flakyCategoryServer struct {
	*fakeCategoryServer

	// failuresBeforeSuccess is how many attempts fail before one succeeds.
	failuresBeforeSuccess int
	// attempts counts every call, so a test can assert the retry actually happened
	// rather than inferring it from the outcome.
	attempts int
	// lastAttemptDeadline records the deadline the most recent attempt ran under.
	lastAttemptDeadline time.Time
	// hadDeadline reports whether that attempt's context carried a deadline at all.
	hadDeadline bool
}

func newFlakyCategoryServer(failures int) *flakyCategoryServer {
	return &flakyCategoryServer{
		fakeCategoryServer: &fakeCategoryServer{
			categories: map[int64]*categorypb.Category{
				1: {Id: 1, Name: "Electronics"},
			},
		},
		failuresBeforeSuccess: failures,
	}
}

func (f *flakyCategoryServer) GetCategory(ctx context.Context, req *categorypb.GetCategoryRequest, _ ...grpc.CallOption) (*categorypb.GetCategoryResponse, error) {
	f.attempts++
	f.lastAttemptDeadline, f.hadDeadline = ctx.Deadline()

	// An explicit error set by a test wins over the failure count, so the same double
	// serves both the transient and the permanent-failure cases.
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.attempts <= f.failuresBeforeSuccess {
		// Unavailable is the canonical transient code: the peer is there but cannot
		// answer right now.
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	return &categorypb.GetCategoryResponse{Category: f.categories[req.GetId()]}, nil
}

// fastReadPolicy retries without waiting, so a retry test costs microseconds rather
// than the hundreds of milliseconds a real backoff would take.
func fastReadPolicy(attempts int) retry.Policy {
	return retry.New(retry.Options{
		MaxAttempts: attempts,
		BaseDelay:   time.Nanosecond,
		MaxDelay:    time.Nanosecond,
		Rand:        func() float64 { return 0 },
	})
}

func TestTransientFailureIsRetriedAndSucceeds(t *testing.T) {
	// The whole point of the retry loop: a dependency that blipped is answered without
	// the client ever seeing a failure.
	server := newFlakyCategoryServer(2)
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(3),
		guard:   testBreaker("category-service"),
	}

	got, err := client.GetCategory(t.Context(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Electronics" {
		t.Fatalf("got %+v, want the category", got)
	}
	if server.attempts != 3 {
		t.Fatalf("the dependency saw %d attempts, want 3", server.attempts)
	}
}

func TestRetryStopsAtMaxAttempts(t *testing.T) {
	server := newFlakyCategoryServer(1_000)
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(3),
		guard:   testBreaker("category-service"),
	}

	_, err := client.GetCategory(t.Context(), "1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if server.attempts != 3 {
		t.Fatalf("the dependency saw %d attempts, want exactly MaxAttempts=3", server.attempts)
	}
	// The error must still reach the caller classified, because the GraphQL error
	// presenter and the breaker both key off that classification.
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want it classified as retryable", err)
	}
}

func TestThreeAttemptsAreOneFailureToTheBreaker(t *testing.T) {
	// The SRE workbook's central recommendation, and the mistake most likely when retry
	// and breaker are introduced together: count failures at exactly one layer. Three
	// attempts of one user request must be one failure, or a breaker configured to open
	// after a single failure would trip on one unlucky request rather than a trend.
	server := newFlakyCategoryServer(1_000)
	guard := breaker.New(breaker.Options{
		Name:             "category-service",
		FailureThreshold: 2,
		Cooldown:         time.Minute,
	})
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(3),
		guard:   guard,
	}

	_, _ = client.GetCategory(t.Context(), "1")

	if server.attempts != 3 {
		t.Fatalf("the dependency saw %d attempts, want 3", server.attempts)
	}
	state, failures, _ := guard.Snapshot()
	if failures != 1 {
		t.Fatalf("the breaker counted %d failures for one request, want 1", failures)
	}
	// Two is the threshold, so one request must not have opened it.
	if state != breaker.StateClosed {
		t.Fatalf("state %v, want CLOSED: one request is not a trend", state)
	}
}

func TestEachAttemptGetsItsOwnDeadline(t *testing.T) {
	// The retry policy wraps the deadline rather than the other way round. A deadline
	// outside the retry loop would divide one timeout between all three attempts, so
	// the second would get whatever the first left behind — which for a 100ms first
	// attempt against a 300ms timeout is nothing, and the retry is then pointless.
	server := newFlakyCategoryServer(2)
	client := &categoryClient{
		api:     server,
		timeout: 200 * time.Millisecond,
		policy:  fastReadPolicy(3),
		guard:   testBreaker("category-service"),
	}

	if _, err := client.GetCategory(t.Context(), "1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !server.hadDeadline {
		t.Fatal("the attempt context carried no deadline, so a stalled dependency could hold the request open indefinitely")
	}
	if remaining := time.Until(server.lastAttemptDeadline); remaining > 200*time.Millisecond {
		t.Fatalf("the last attempt had %s of budget, want at most the 200ms per-attempt timeout", remaining)
	}
}

func TestAnEarlierCallerDeadlineIsNotLoosened(t *testing.T) {
	// The per-attempt timeout must never extend the caller's budget. A resolver that set
	// a 3s call timeout under a 2s request budget would otherwise let the request live
	// 3s, which is exactly the failure the request budget exists to prevent.
	server := newFlakyCategoryServer(1_000)
	client := &categoryClient{
		api:     server,
		timeout: 3 * time.Second,
		policy:  fastReadPolicy(3),
		guard:   testBreaker("category-service"),
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, _ = client.GetCategory(ctx, "1")

	// The caller's budget is gone, so the recorded attempt deadline should be at or
	// before the caller's, not three seconds beyond it.
	if server.lastAttemptDeadline.After(time.Now().Add(150 * time.Millisecond)) {
		t.Fatalf("the attempt deadline is %s out, beyond the caller's 100ms budget",
			server.lastAttemptDeadline.Sub(time.Now()))
	}
}

func TestTheCallersBudgetBoundsEveryRetry(t *testing.T) {
	// Two calls sharing one request budget. Without the deadline check inside the retry
	// loop, three attempts of 100ms each would run 300ms per call and 600ms for the
	// pair, comfortably past the 250ms budget the caller set.
	server := newFlakyCategoryServer(1_000)
	client := &categoryClient{
		api:     server,
		timeout: 100 * time.Millisecond,
		policy:  fastReadPolicy(3),
		guard:   testBreaker("category-service"),
	}

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _ = client.GetCategory(ctx, "1")
	_, _ = client.GetCategory(ctx, "1")
	elapsed := time.Since(start)

	// Generous enough not to flake on a loaded machine, tight enough to fail if the
	// retries ignore the budget entirely (which would be ~600ms).
	if elapsed > 500*time.Millisecond {
		t.Fatalf("two calls took %s against a 250ms budget; the retries are not bounded by the caller's deadline", elapsed)
	}
}

func TestOpenBreakerRefusesWithoutCallingTheDependency(t *testing.T) {
	// The property the breaker exists for. Asserting only on the returned error would
	// pass even if the call still went out, which is the failure that matters: the whole
	// purpose is to not make the call.
	server := newFlakyCategoryServer(1_000)
	guard := breaker.New(breaker.Options{
		Name:             "category-service",
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	})
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(3),
		guard:   guard,
	}

	// One request trips it.
	_, _ = client.GetCategory(t.Context(), "1")
	attemptsAfterTrip := server.attempts

	_, err := client.GetCategory(t.Context(), "1")
	if !errors.Is(err, breaker.ErrOpenCircuit) {
		t.Fatalf("got %v, want ErrOpenCircuit", err)
	}
	if server.attempts != attemptsAfterTrip {
		t.Fatalf("the dependency was called %d more times through an open breaker",
			server.attempts-attemptsAfterTrip)
	}
}

func TestBreakerRecoversThroughHalfOpen(t *testing.T) {
	// The full cycle: trip, cool down, probe, recover. A breaker that opens and never
	// closes is a self-inflicted outage lasting as long as the process does, and it is
	// the most common way a breaker is wired wrong.
	server := newFlakyCategoryServer(1_000)
	guard := breaker.New(breaker.Options{
		Name:             "category-service",
		FailureThreshold: 1,
		Cooldown:         10 * time.Millisecond,
		HalfOpenProbes:   1,
		ProbeTimeout:     time.Second,
	})
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(1),
		guard:   guard,
	}

	_, _ = client.GetCategory(t.Context(), "1")
	if got := guard.State(); got != breaker.StateOpen {
		t.Fatalf("state %v, want OPEN", got)
	}

	// Let the cooldown elapse and let the dependency recover.
	time.Sleep(20 * time.Millisecond)
	server.failuresBeforeSuccess = 0

	if _, err := client.GetCategory(t.Context(), "1"); err != nil {
		t.Fatalf("the half-open probe failed: %v", err)
	}
	if got := guard.State(); got != breaker.StateClosed {
		t.Fatalf("state %v, want CLOSED after a successful probe", got)
	}
}

func TestNonRetryableErrorIsNotRetried(t *testing.T) {
	// The service answered correctly. Retrying spends the caller's remaining budget to
	// be told the same thing again.
	server := newFlakyCategoryServer(0)
	server.getErr = status.Error(codes.InvalidArgument, "bad request")
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(5),
		guard:   testBreaker("category-service"),
	}

	_, err := client.GetCategory(t.Context(), "1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if server.attempts != 1 {
		t.Fatalf("the dependency saw %d attempts for a client error, want 1", server.attempts)
	}
}

func TestClientErrorsDoNotTripTheBreaker(t *testing.T) {
	// The breaker counts only retryable failures. A service correctly rejecting a bad
	// request is not a fault, and a breaker that opened on that would shed traffic
	// during a period in which the dependency is entirely healthy.
	server := newFlakyCategoryServer(0)
	server.getErr = status.Error(codes.NotFound, "no such category")
	guard := breaker.New(breaker.Options{
		Name:             "category-service",
		FailureThreshold: 2,
		Cooldown:         time.Hour,
	})
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(5),
		guard:   guard,
	}

	for range 10 {
		_, _ = client.GetCategory(t.Context(), "1")
	}

	if got := guard.State(); got != breaker.StateClosed {
		t.Fatalf("state %v, want CLOSED: ten not-founds is not an outage", got)
	}
}

func TestWriteClientMakesOneAttemptOnATransientFailure(t *testing.T) {
	// The most consequential assertion in this file. product-service has no idempotency
	// key on CreateProduct, so a retry after a call that succeeded but whose response
	// was lost creates a second product. A test that only checked the policy's
	// configuration would not catch a write client wired with the read policy.
	server := &fakeProductServer{err: status.Error(codes.Unavailable, "connection refused")}
	client := &productClient{
		api:     server,
		timeout: time.Second,
		policy:  retry.Sensitive(),
		guard:   testBreaker("product-service"),
	}

	_, err := client.CreateProduct(t.Context(), model.CreateProductInput{
		CategoryID: "1",
		Name:       "Aurora Lamp",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(server.createCalls) != 1 {
		t.Fatalf("the write dependency saw %d attempts, want exactly 1", len(server.createCalls))
	}
}

func TestWriteClientDoesNotRetryADeadline(t *testing.T) {
	// The same rule for the other transient-looking failure. A timed-out create is the
	// most dangerous case to retry, because the write may well have landed.
	server := &fakeProductServer{err: status.Error(codes.DeadlineExceeded, "took too long")}
	client := &productClient{
		api:     server,
		timeout: time.Second,
		policy:  retry.Sensitive(),
		guard:   testBreaker("product-service"),
	}

	_, _ = client.CreateProduct(t.Context(), model.CreateProductInput{
		CategoryID: "1",
		Name:       "Aurora Lamp",
	})

	if len(server.createCalls) != 1 {
		t.Fatalf("the write dependency saw %d attempts after a timeout, want exactly 1", len(server.createCalls))
	}
}

func TestCancelledContextStopsRetryingBeforeTheFirstAttempt(t *testing.T) {
	// A caller that has gone away should not have work done on its behalf — not even
	// the first attempt, which is the case that a retry loop checking only after the
	// call would get wrong.
	server := newFlakyCategoryServer(1_000)
	client := &categoryClient{
		api:     server,
		timeout: time.Second,
		policy:  fastReadPolicy(5),
		guard:   testBreaker("category-service"),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, _ = client.GetCategory(ctx, "1")

	if server.attempts != 0 {
		t.Fatalf("the dependency was called %d times against a cancelled context, want 0", server.attempts)
	}
}

func TestACallWithNoWiringStillMakesExactlyOneAttempt(t *testing.T) {
	// A client built without a policy or breaker still has to call through. A zero-value
	// retry policy with a loop bounded by MaxAttempts=0 would never run and would report
	// success without having attempted anything — the worst possible failure for a
	// transport layer, because it looks like a working service.
	server := newFlakyCategoryServer(0)
	client := &categoryClient{api: server, timeout: time.Second}

	got, err := client.GetCategory(t.Context(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Electronics" {
		t.Fatalf("got %+v", got)
	}
	if server.attempts != 1 {
		t.Fatalf("the dependency saw %d attempts, want 1", server.attempts)
	}
}
