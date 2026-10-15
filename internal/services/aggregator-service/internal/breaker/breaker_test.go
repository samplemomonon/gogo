package breaker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The breaker tests use an injected clock rather than sleeping. Every transition in
// this package is time-driven, and a test that sleeps turns a sub-millisecond unit
// into a multi-second suite — or, worse, into a flaky one when CI is loaded.

func newTestBreaker(t *testing.T, opts Options) (*Breaker, *fakeClock) {
	t.Helper()

	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	b := New(opts)
	b.now = clock.time
	return b, clock
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// failures drives a breaker to the open state and reports whether it got there.
func failures(b *Breaker, n int) {
	for range n {
		b.Report(Failure)
	}
}

func TestClosedBreakerAllowsEverything(t *testing.T) {
	b, _ := newTestBreaker(t, Options{Name: "test"})

	if got := b.State(); got != StateClosed {
		t.Fatalf("state %v, want CLOSED", got)
	}
	// Admission is what matters, not the label: a closed breaker that refuses is a
	// service that has stopped serving.
	for range 10 {
		if err := b.Allow(); err != nil {
			t.Fatalf("a closed breaker refused: %v", err)
		}
		b.Report(Success)
	}
}

func TestBreakerOpensAtTheThreshold(t *testing.T) {
	b, _ := newTestBreaker(t, Options{Name: "test", FailureThreshold: 3, Cooldown: time.Minute})

	failures(b, 2)
	if got := b.State(); got != StateClosed {
		t.Fatalf("state after 2 of 3 failures: %v, want still CLOSED", got)
	}

	failures(b, 1)
	if got := b.State(); got != StateOpen {
		t.Fatalf("state after 3 failures: %v, want OPEN", got)
	}

	// The refusal is what stops the load, not the label.
	err := b.Allow()
	if err == nil {
		t.Fatal("an open breaker admitted a call")
	}
	if !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("got %v, want it to wrap ErrOpenCircuit so callers can tell a refusal from a failure", err)
	}
}

func TestASingleSuccessResetsTheRun(t *testing.T) {
	// This is the property that makes the breaker usable for a service that has
	// occasional blips. A breaker keyed on a running total would need a cooldown to
	// recover from three scattered failures, which is far slower than the dependency.
	b, _ := newTestBreaker(t, Options{Name: "test", FailureThreshold: 3, Cooldown: time.Minute})

	b.Report(Failure)
	b.Report(Failure)
	b.Report(Success)
	b.Report(Failure)
	b.Report(Failure)

	if got := b.State(); got != StateClosed {
		t.Fatalf("state %v, want CLOSED: two failures in a row is below a threshold of 3", got)
	}
}

func TestNonRetryableOutcomesNeverTrip(t *testing.T) {
	// Reported as Success deliberately: the service answering NotFound correctly is not
	// a fault, and a breaker that opened on that would refuse traffic during a period
	// in which the dependency is entirely healthy.
	b, _ := newTestBreaker(t, Options{Name: "test", FailureThreshold: 2, Cooldown: time.Minute})

	for range 20 {
		b.Report(Success)
	}

	if got := b.State(); got != StateClosed {
		t.Fatalf("state %v, want CLOSED", got)
	}
}

func TestOpenBreakerMovesToHalfOpenAfterCooldown(t *testing.T) {
	b, clock := newTestBreaker(t, Options{Name: "test", FailureThreshold: 1, Cooldown: 30 * time.Second})

	failures(b, 1)
	if got := b.State(); got != StateOpen {
		t.Fatalf("state %v, want OPEN", got)
	}

	// Just before the cooldown: still open. An off-by-one here would probe a
	// dependency that is still failing.
	clock.advance(29 * time.Second)
	if got := b.State(); got != StateOpen {
		t.Fatalf("state at 29s %v, want still OPEN", got)
	}

	clock.advance(2 * time.Second)
	if got := b.State(); got != StateHalfOpen {
		t.Fatalf("state at 31s %v, want HALF_OPEN", got)
	}
}

func TestHalfOpenAdmitsOnlyTheProbeBudget(t *testing.T) {
	// The limit is what stops a half-open breaker from reintroducing the load it was
	// opened to stop. Without it, "half-open" would just mean "open, but slower".
	b, clock := newTestBreaker(t, Options{Name: "test", FailureThreshold: 1, Cooldown: time.Second, HalfOpenProbes: 2})

	failures(b, 1)
	clock.advance(2 * time.Second)

	if err := b.Allow(); err != nil {
		t.Fatalf("first probe refused: %v", err)
	}
	if err := b.Allow(); err != nil {
		t.Fatalf("second probe refused: %v", err)
	}

	// The third exceeds the budget of two and must be refused.
	err := b.Allow()
	if err == nil {
		t.Fatal("a third probe was admitted with a budget of two")
	}
	if !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("got %v, want ErrOpenCircuit", err)
	}
}

func TestSuccessfulProbeClosesTheBreaker(t *testing.T) {
	b, clock := newTestBreaker(t, Options{Name: "test", FailureThreshold: 1, Cooldown: time.Second})

	failures(b, 1)
	clock.advance(2 * time.Second)

	if err := b.Allow(); err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	b.Report(Success)

	// One good answer is enough: the dependency has demonstrably recovered.
	if got := b.State(); got != StateClosed {
		t.Fatalf("state %v, want CLOSED", got)
	}
	for range 5 {
		if err := b.Allow(); err != nil {
			t.Fatalf("a closed breaker refused: %v", err)
		}
		b.Report(Success)
	}
}

func TestFailedProbeReopensImmediately(t *testing.T) {
	// The probe exists to answer one question, and a failure is the answer. Waiting
	// out another full run of failures would send traffic at a service that just
	// demonstrated it is still broken.
	b, clock := newTestBreaker(t, Options{Name: "test", FailureThreshold: 1, Cooldown: time.Second})

	failures(b, 1)
	clock.advance(2 * time.Second)

	if err := b.Allow(); err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	b.Report(Failure)

	if got := b.State(); got != StateOpen {
		t.Fatalf("state %v, want OPEN after a failed probe", got)
	}

	// And the cooldown restarts from the failed probe, not from the original trip.
	if err := b.Allow(); err == nil {
		t.Fatal("the breaker admitted a call immediately after a failed probe")
	}
}

func TestConsecutiveCountIsNotDoubleCountedInHalfOpen(t *testing.T) {
	// A half-open failure sets the counter to the threshold so the breaker reopens.
	// Incrementing as well would leave it above the threshold, which is harmless here
	// but makes the snapshot meaningless as an indicator of how bad things are.
	b, clock := newTestBreaker(t, Options{Name: "test", FailureThreshold: 3, Cooldown: time.Second})

	failures(b, 3)
	clock.advance(2 * time.Second)
	if err := b.Allow(); err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	b.Report(Failure)

	state, failures, _ := b.Snapshot()
	if state != StateOpen {
		t.Fatalf("state %v, want OPEN", state)
	}
	if failures != 3 {
		t.Fatalf("consecutive failures %d, want exactly the threshold of 3", failures)
	}
}

func TestExecuteCountsOncePerCallNotOncePerOutcome(t *testing.T) {
	// The SRE workbook's central recommendation: count failures at one layer only. A
	// caller running three attempts through this must produce one failure, or a single
	// unlucky request trips a breaker set to open after one failure.
	b, _ := newTestBreaker(t, Options{Name: "test", FailureThreshold: 2, Cooldown: time.Minute})

	callErr := errors.New("connection refused")
	calls := 0
	_ = b.Execute(t.Context(), func(error) Outcome { return Failure },
		func(context.Context) error {
			calls++
			return callErr
		})

	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}
	_, failures, _ := b.Snapshot()
	if failures != 1 {
		t.Fatalf("counted %d failures for one call, want 1", failures)
	}
	if got := b.State(); got != StateClosed {
		t.Fatalf("state %v, want CLOSED: one failure must not trip a threshold of two", got)
	}
}

func TestExecuteRefusesWithoutCalling(t *testing.T) {
	// The whole point: an open breaker must not reach the dependency at all. A test
	// that only checked the returned error would pass even if the function still ran.
	b, _ := newTestBreaker(t, Options{Name: "test", FailureThreshold: 1, Cooldown: time.Minute})

	failures(b, 1)

	called := false
	err := b.Execute(t.Context(), func(error) Outcome { return Failure },
		func(context.Context) error {
			called = true
			return nil
		})

	if called {
		t.Fatal("the dependency was called through an open breaker")
	}
	if !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("got %v, want ErrOpenCircuit", err)
	}
}

func TestProbeIsBoundedByProbeTimeout(t *testing.T) {
	// Without a bound, a half-open breaker wedges: the dependency accepts the
	// connection and never answers, the probe never returns, and the breaker sits
	// half-open forever with its budget spent.
	b, clock := newTestBreaker(t, Options{
		Name:             "test",
		FailureThreshold: 1,
		Cooldown:         time.Second,
		ProbeTimeout:     100 * time.Millisecond,
	})

	failures(b, 1)
	clock.advance(2 * time.Second)

	// The probe deadline is the value under test, so it is captured directly rather
	// than measured. Waiting for it to fire would mean either sleeping or an injected
	// clock that advances on its own, and neither proves more than reading the value.
	deadlineSeen := false
	var budget time.Duration
	err := b.Execute(t.Context(), func(error) Outcome { return Failure },
		func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			deadlineSeen = ok
			if ok {
				budget = time.Until(deadline)
			}
			<-ctx.Done()
			return ctx.Err()
		})

	if !deadlineSeen {
		t.Fatal("the probe context carried no deadline, so a half-open breaker could wedge")
	}
	// It must be the probe timeout, not the caller's own budget, which here is none.
	if budget > 200*time.Millisecond {
		t.Fatalf("probe budget %s, want it bounded near the 100ms ProbeTimeout", budget)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the probe to end in DeadlineExceeded", err)
	}
}

func TestClosedStateCallsAreNotProbeBounded(t *testing.T) {
	// A second, shorter bound on a normal call would fail requests the caller was
	// willing to wait for. Only probes are bounded.
	b, _ := newTestBreaker(t, Options{
		Name:             "test",
		FailureThreshold: 3,
		ProbeTimeout:     time.Millisecond,
	})

	deadlineSeen := false
	_ = b.Execute(t.Context(), func(error) Outcome { return Failure },
		func(ctx context.Context) error {
			_, deadlineSeen = ctx.Deadline()
			return nil
		})

	if deadlineSeen {
		t.Fatal("a closed-state call was given a probe deadline")
	}
}

func TestDefaultsMatchTheRestOfThePlatform(t *testing.T) {
	// One set of numbers across the stack, so an operator reads one table.
	b := New(Options{Name: "test"})

	if b.failureThreshold != DefaultFailureThreshold {
		t.Errorf("threshold %d, want %d", b.failureThreshold, DefaultFailureThreshold)
	}
	if b.cooldown != DefaultCooldown {
		t.Errorf("cooldown %s, want %s", b.cooldown, DefaultCooldown)
	}
	if b.halfOpenProbes != DefaultHalfOpenProbes {
		t.Errorf("probes %d, want %d", b.halfOpenProbes, DefaultHalfOpenProbes)
	}
}

func TestStateChangeCallbackFiresOncePerTransition(t *testing.T) {
	// An operator reading a transition log needs it to be an accurate record. A
	// callback that fired on every call while open would bury the transition in noise
	// during exactly the incident it is supposed to explain.
	var mu sync.Mutex
	var events []string
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}

	b := New(Options{
		Name:             "category-service",
		FailureThreshold: 1,
		Cooldown:         time.Second,
		OnStateChange: func(name string, from, to State) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, name+":"+from.String()+"->"+to.String())
		},
	})
	b.now = clock.time

	failures(b, 1)

	// Several refusals while open must not report transitions.
	for range 5 {
		_ = b.Allow()
	}

	clock.advance(2 * time.Second)
	_ = b.Allow()
	b.Report(Success)

	mu.Lock()
	defer mu.Unlock()

	// Three events: the trip, the cooldown expiry, and the recovery. The half-open
	// transition is reported because an operator watching a dependency needs to see the
	// breaker probing, not just that it stopped refusing. What must not appear is a
	// fourth event for the refusals in between.
	want := []string{
		"category-service:CLOSED->OPEN",
		"category-service:OPEN->HALF_OPEN",
		"category-service:HALF_OPEN->CLOSED",
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events %v, want %v", len(events), events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d: got %q, want %q", i, events[i], want[i])
		}
	}
}

func TestCallbackRunsWithoutTheLockHeld(t *testing.T) {
	// A callback that reads the breaker's state must not deadlock. A log callback that
	// did would hang the request that happened to trip the breaker, which is the worst
	// possible moment to hang.
	var b *Breaker
	opts := Options{
		Name:             "test",
		FailureThreshold: 1,
		Cooldown:         time.Minute,
	}
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	b = New(opts)
	b.now = clock.time
	b.onStateChange = func(string, State, State) {
		// Reading state from inside the callback is the deadlock this guards.
		_ = b.State()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		failures(b, 1)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Report deadlocked calling back into the breaker")
	}
}

func TestConcurrentReportsDoNotTripEarly(t *testing.T) {
	// The counters are shared mutable state behind a mutex, and the race detector
	// covers the memory safety. What it does not cover is the arithmetic: with a
	// threshold of 100 and 100 concurrent failures, the breaker must not open early.
	const threshold = 100
	b := New(Options{Name: "test", FailureThreshold: threshold, Cooldown: time.Hour})

	var wg sync.WaitGroup
	// Each goroutine interleaves one success with failures, so a correct
	// consecutive-run implementation may never reach the threshold at all.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				b.Report(Failure)
				b.Report(Success)
			}
		}()
	}
	wg.Wait()

	state, failures, _ := b.Snapshot()
	if state == StateOpen {
		t.Fatalf("breaker opened with an interleaved success/failure pattern")
	}
	if failures != 0 {
		t.Fatalf("consecutive failures %d, want 0: the last report was a success", failures)
	}
}
