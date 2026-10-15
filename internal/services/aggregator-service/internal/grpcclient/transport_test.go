package grpcclient

import (
	"testing"
	"time"

	"aggregator-service/internal/breaker"
	"aggregator-service/internal/retry"
)

// A peer that is not listening is the case where a client's transport settings matter
// most, and it is the one that can be measured without any of the other services
// running. These tests exist because the transport is configured for fail-fast, and
// fail-fast looks like a mistake to anyone reading it without the reason — the usual
// instinct for a resilient client is to reach for WaitForReady first.
//
// They dial through New rather than through a fake, because the setting under test lives
// on the connection New creates. A test that substituted the generated client would pass
// with WaitForReady enabled, which is exactly the regression these exist to catch.

// deadTargets points every dependency at a reserved port that nothing listens on.
//
// Port 1 is reserved and has no service, so this needs no fixtures and cannot be made to
// pass by accident.
func deadTargets() Targets {
	return Targets{
		Category: "127.0.0.1:1",
		Media:    "127.0.0.1:1",
		Product:  "127.0.0.1:1",
		Listing:  "127.0.0.1:1",
		Search:   "127.0.0.1:1",
		Timeout:  3 * time.Second,
		ReadPolicy: retry.New(retry.Options{
			MaxAttempts: 3,
			BaseDelay:   time.Nanosecond,
			MaxDelay:    time.Nanosecond,
			Rand:        func() float64 { return 0 },
		}),
		Breaker: breaker.Options{
			Name:             "unused",
			FailureThreshold: 5,
			Cooldown:         time.Hour,
		},
	}
}

// TestARefusedPeerFailsFast is the assertion behind the transport configuration.
//
// With WaitForReady(true), a refused connection makes the call block until its context
// expires: three seconds here, against the attempt budget. Without it, the same call
// returns immediately. The difference is not cosmetic — TestFailFastKeepsTheBreakerResponsive
// is what it costs.
func TestARefusedPeerFailsFast(t *testing.T) {
	clients, err := New(deadTargets())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clients.Close()

	start := time.Now()
	_, err = clients.Category.GetCategory(t.Context(), "1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a call to a dead peer succeeded")
	}
	// Generous enough not to flake under load, and far below the 3s per-attempt budget
	// times three attempts. The point is that it returned without waiting for a deadline.
	if elapsed > time.Second {
		t.Fatalf("three attempts against a refusing peer took %s, want them to fail immediately: "+
			"an attempt that costs the whole timeout is multiplied by the attempt budget and delays the breaker", elapsed)
	}
}

// TestFailFastKeepsTheBreakerResponsive measures the property that actually matters
// operationally: how long the breaker takes to notice a dependency is down.
//
// With a 3s attempt timeout and three attempts, a fail-slow transport spends 9s per user
// request on a dependency that is refusing connections, and a breaker set to open after
// five consecutive failures needs 45s of traffic before it trips — which is the whole
// incident. Fail-fast collapses both. The assertion is about orders of magnitude rather
// than a precise threshold, because the fast path is microseconds and the slow path is
// seconds.
func TestFailFastKeepsTheBreakerResponsive(t *testing.T) {
	const threshold = 5
	targets := deadTargets()
	targets.Breaker.FailureThreshold = threshold

	clients, err := New(targets)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clients.Close()

	breakers := clients.Breakers()
	listing, ok := breakers["product-listing-service"]
	if !ok {
		t.Fatalf("no breaker registered for product-listing-service; have %v", breakerNames(breakers))
	}

	start := time.Now()
	for range threshold {
		// Each request reports exactly one failure to the breaker regardless of how many
		// attempts it took, so the breaker trips after exactly threshold requests.
		_, _ = clients.Listing.ListProducts(t.Context(), ProductFilter{})
	}
	elapsed := time.Since(start)

	if got := listing.State(); got != breaker.StateOpen {
		t.Fatalf("breaker state %v after %d failing requests, want OPEN", got, threshold)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the breaker took %s to trip on a dependency refusing connections instantly; "+
			"every request in that window paid the full attempt timeout", elapsed)
	}
}

// TestAnOpenBreakerShedsWithoutTouchingTheNetwork measures the win the breaker exists
// for, now that the transport fails fast.
//
// With fail-fast, requests before the breaker trips are cheap but still real gRPC calls.
// After it trips they are refused outright. The second half of the loop must be
// dramatically faster, and it must remain faster even though the port is refusing
// immediately — which is the case a fail-slow transport would have hidden, by making
// both halves equally slow.
func TestAnOpenBreakerShedsWithoutTouchingTheNetwork(t *testing.T) {
	targets := deadTargets()
	targets.Breaker.FailureThreshold = 1
	targets.ReadPolicy = retry.Sensitive()

	clients, err := New(targets)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clients.Close()

	// Trip it.
	_, _ = clients.Listing.ListProducts(t.Context(), ProductFilter{})

	listing := clients.Breakers()["product-listing-service"]
	if got := listing.State(); got != breaker.StateOpen {
		t.Fatalf("breaker state %v after one failing request, want OPEN", got)
	}

	shed := 0
	start := time.Now()
	for range 50 {
		if _, err := clients.Listing.ListProducts(t.Context(), ProductFilter{}); err == nil {
			t.Fatal("a call through an open breaker succeeded")
		} else {
			shed++
		}
	}
	elapsed := time.Since(start)

	if shed != 50 {
		t.Fatalf("%d of 50 calls were shed", shed)
	}
	// Fifty refused calls must not cost fifty dial attempts. Even on loopback that is a
	// wide margin.
	if elapsed > time.Second {
		t.Fatalf("50 calls through an open breaker took %s; the breaker is not refusing before "+
			"the attempt, so every request is still paying for a dial", elapsed)
	}
}

// breakerNames is a failure-message helper: the test cares which breakers exist, and a
// bare %v on a map of pointers is unreadable.
func breakerNames(breakers map[string]*breaker.Breaker) []string {
	out := make([]string, 0, len(breakers))
	for name := range breakers {
		out = append(out, name)
	}
	return out
}
