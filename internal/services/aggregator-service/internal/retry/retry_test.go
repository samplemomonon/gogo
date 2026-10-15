package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
)

// Every test here controls its own timing. The retry loop's whole behaviour is a
// function of the wait between attempts, so a test that lets real time pass is
// testing the scheduler as much as the policy.

// deterministic makes the jitter reproducible, so backoff assertions are exact.
func deterministic(value float64) func() float64 {
	return func() float64 { return value }
}

// noDelay removes the wait entirely, so a retry test costs microseconds.
func noDelay(maxAttempts int) Policy {
	return New(Options{
		MaxAttempts: maxAttempts,
		BaseDelay:   time.Nanosecond,
		MaxDelay:    time.Nanosecond,
		Rand:        deterministic(0),
	})
}

var errTransient = errors.Join(apperror.ErrUnavailable, errors.New("connection refused"))

func TestSucceedsWithoutRetrying(t *testing.T) {
	calls := 0
	policy := noDelay(3)

	err := policy.Do(t.Context(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The happy path must not pay for the machinery. A retry policy that always slept
	// before its first attempt would add latency to every successful request.
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}
}

func TestRetriesTransientFailuresThenSucceeds(t *testing.T) {
	calls := 0
	policy := noDelay(3)

	err := policy.Do(t.Context(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errTransient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("the function ran %d times, want 3", calls)
	}
}

func TestStopsAtMaxAttempts(t *testing.T) {
	calls := 0
	policy := noDelay(3)

	err := policy.Do(t.Context(), func(context.Context) error {
		calls++
		return errTransient
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 3 {
		t.Fatalf("the function ran %d times, want exactly MaxAttempts=3", calls)
	}
}

func TestDoesNotRetryNonRetryableErrors(t *testing.T) {
	// A client mistake gets the same answer every time. Retrying it spends the
	// caller's remaining budget to be told the same thing again.
	calls := 0
	policy := noDelay(3)

	err := policy.Do(t.Context(), func(context.Context) error {
		calls++
		return apperror.ErrInvalidInput
	})
	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("got %v, want the original error", err)
	}
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}
}

func TestDoesNotRetryAnOpenCircuit(t *testing.T) {
	// The interaction that matters most. Sleeping and retrying against a dependency the
	// breaker has already judged unable to take traffic is the load amplification the
	// two mechanisms exist to prevent between them.
	calls := 0
	policy := noDelay(5)

	err := policy.Do(t.Context(), func(context.Context) error {
		calls++
		return errors.Join(breaker.ErrOpenCircuit, apperror.ErrUnavailable)
	})
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}
	if !errors.Is(err, breaker.ErrOpenCircuit) {
		t.Fatalf("got %v, want ErrOpenCircuit", err)
	}
}

func TestDoesNotRetryWhenTheCallerCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	calls := 0
	policy := noDelay(3)

	err := policy.Do(ctx, func(context.Context) error {
		calls++
		return errTransient
	})
	// A caller that has gone away should get no attempts at all, not even the first.
	if calls != 0 {
		t.Fatalf("the function ran %d times against a cancelled context, want 0", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestSpendsNoAttemptWhenTheBudgetIsAlreadyGone(t *testing.T) {
	// An expired context must produce zero calls rather than one call plus retries.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	calls := 0
	_ = noDelay(3).Do(ctx, func(context.Context) error {
		calls++
		return nil
	})
	if calls != 0 {
		t.Fatalf("the function ran %d times with an expired budget, want 0", calls)
	}
}

func TestWillNotRetryWhenTheWaitCannotFit(t *testing.T) {
	// The budget, not the attempt count, is the real constraint. A retry that runs past
	// the deadline is not a retry, it is latency added to a request that has already
	// timed out, held open on the caller's goroutine.
	policy := New(Options{
		MaxAttempts: 5,
		BaseDelay:   400 * time.Millisecond,
		MaxDelay:    400 * time.Millisecond,
		Rand:        deterministic(1), // the top of the window, the worst case
	})

	// A budget that cannot accommodate one wait, let alone several.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	calls := 0
	err := policy.Do(ctx, func(context.Context) error {
		calls++
		return errTransient
	})
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1: the retry could not fit the budget", calls)
	}
	// The underlying failure is more useful than a deadline error that hides it.
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want the underlying failure rather than a deadline error", err)
	}
}

func TestRetryableDistinguishesAttemptTimeoutFromSpentBudget(t *testing.T) {
	// Both surface as context.DeadlineExceeded and they mean opposite things. A
	// per-attempt timeout says the dependency is slow and worth another try; an
	// exhausted budget says there is no time left to try in.
	attemptTimedOut := context.DeadlineExceeded
	if !Retryable(t.Context(), attemptTimedOut) {
		t.Fatal("an attempt timeout with budget remaining should be retryable")
	}

	spent, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // let it expire

	if Retryable(spent, attemptTimedOut) {
		t.Fatal("a deadline error with no budget left must not be retryable")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	policy := New(Options{
		MaxAttempts: 10,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    80 * time.Millisecond,
		Rand:        deterministic(1),
	})

	want := []time.Duration{
		10 * time.Millisecond, // attempt 1 -> base
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		80 * time.Millisecond, // capped
		80 * time.Millisecond,
	}
	for i, expected := range want {
		if got := policy.backoff(i + 1); got != expected {
			t.Errorf("backoff before attempt %d: got %s, want %s", i+2, got, expected)
		}
	}
}

func TestBackoffJittersAcrossTheWholeWindow(t *testing.T) {
	// Without jitter, every request that failed at the same instant retries at the same
	// instant, and the retry wave reproduces the failure it is recovering from.
	var samples []time.Duration
	for _, sample := range []float64{0, 0.25, 0.5, 0.75, 1} {
		policy := New(Options{
			MaxAttempts: 3,
			BaseDelay:   100 * time.Millisecond,
			MaxDelay:    time.Second,
			Rand:        deterministic(sample),
		})
		samples = append(samples, policy.backoff(1))
	}

	if samples[0] != 0 {
		t.Errorf("a sample of 0 should give no wait, got %s", samples[0])
	}
	if samples[len(samples)-1] != 100*time.Millisecond {
		t.Errorf("a sample of 1 should give the full window, got %s", samples[len(samples)-1])
	}
	// Every distinct sample must give a distinct delay, which is the whole point.
	seen := map[time.Duration]bool{}
	for _, s := range samples {
		if seen[s] {
			t.Errorf("duplicate delay %s across samples", s)
		}
		seen[s] = true
	}
}

func TestRandomJitterStaysWithinTheWindow(t *testing.T) {
	// A real rand source must never produce a delay outside the computed window. A
	// negative delay would become an immediate retry and turn the backoff into a busy
	// loop.
	policy := New(Options{MaxAttempts: 20, BaseDelay: time.Millisecond, MaxDelay: time.Second})

	for range 1000 {
		delay := policy.backoff(1)
		if delay < 0 {
			t.Fatalf("negative delay %s", delay)
		}
		if delay > policy.options.MaxDelay {
			t.Fatalf("delay %s exceeded MaxDelay %s", delay, policy.options.MaxDelay)
		}
	}
}

func TestBackoffNeverOverflowsToNegative(t *testing.T) {
	// An unbounded shift would produce a negative duration after ~63 doublings, and a
	// negative timer fires immediately. A large MaxAttempts must not reach that.
	policy := New(Options{MaxAttempts: 1000, BaseDelay: time.Millisecond, MaxDelay: time.Second})

	for attempt := 1; attempt <= 200; attempt++ {
		if delay := policy.backoff(attempt); delay < 0 {
			t.Fatalf("attempt %d produced a negative delay %s", attempt, delay)
		}
	}
}

func TestOnRetryIsCalledBeforeEachRetry(t *testing.T) {
	// The hook is how retries become observable, so it must fire once per retry and not
	// once per attempt.
	type event struct {
		attempt int
		delay   time.Duration
	}
	var events []event

	policy := New(Options{
		MaxAttempts: 3,
		BaseDelay:   time.Nanosecond,
		MaxDelay:    time.Nanosecond,
		Rand:        deterministic(1),
		OnRetry: func(attempt int, delay time.Duration, err error) {
			if !errors.Is(err, apperror.ErrUnavailable) {
				t.Errorf("the hook received %v, want the failure that caused the retry", err)
			}
			events = append(events, event{attempt: attempt, delay: delay})
		},
	})

	calls := 0
	_ = policy.Do(t.Context(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errTransient
		}
		return nil
	})

	// Two failures produced two retries; the third attempt succeeded and was not a retry.
	if len(events) != 2 {
		t.Fatalf("the hook fired %d times, want 2", len(events))
	}
	if events[0].attempt != 1 || events[1].attempt != 2 {
		t.Errorf("attempts %d,%d want 1,2", events[0].attempt, events[1].attempt)
	}
}

func TestSensitivePolicyMakesOneAttempt(t *testing.T) {
	// The reason writes are not retried: product-service has no idempotency key, so a
	// repeated CreateProduct is a second product.
	calls := 0
	err := Sensitive().Do(t.Context(), func(context.Context) error {
		calls++
		return errTransient
	})
	if calls != 1 {
		t.Fatalf("the function ran %d times, want exactly 1 for a write", calls)
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestReadPolicyRetries(t *testing.T) {
	calls := 0
	err := Read().Do(t.Context(), func(context.Context) error {
		calls++
		if calls < 2 {
			return errTransient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("the function ran %d times, want 2", calls)
	}
}

func TestZeroPolicyMakesExactlyOneAttempt(t *testing.T) {
	// A Policy built as a struct literal rather than through New must still call the
	// function. A loop bounded by MaxAttempts=0 would never run and would report
	// success without ever attempting anything, which is the worst possible failure
	// mode for a retry policy.
	calls := 0
	var zero Policy

	err := zero.Do(t.Context(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}

	// And it must not report success after a failure without having tried.
	calls = 0
	err = zero.Do(t.Context(), func(context.Context) error {
		calls++
		return errTransient
	})
	if calls != 1 {
		t.Fatalf("the function ran %d times, want 1", calls)
	}
	if err == nil {
		t.Fatal("a failing attempt was reported as success")
	}
}

func TestOutcomeAgreesWithRetryable(t *testing.T) {
	// The breaker and the retry loop must not disagree about which failures are
	// transient. Two copies of this predicate would drift, and the drift would appear
	// as a breaker that opens on traffic the retry loop considers hopeless.
	ctx := t.Context()

	if got := Outcome(ctx, errTransient); got != breaker.Failure {
		t.Errorf("a transient failure classified as %v, want Failure", got)
	}
	if got := Outcome(ctx, apperror.ErrInvalidInput); got != breaker.Success {
		t.Errorf("a client error classified as %v, want Success", got)
	}
	if got := Outcome(ctx, nil); got != breaker.Success {
		t.Errorf("success classified as %v, want Success", got)
	}
	// An open circuit is not a failure of the dependency from the breaker's point of
	// view; it is the breaker's own decision, and counting it would double-count.
	if got := Outcome(ctx, breaker.ErrOpenCircuit); got != breaker.Success {
		t.Errorf("an open circuit classified as %v, want Success", got)
	}
}

func TestConcurrentPoliciesDoNotShareState(t *testing.T) {
	// Policies are values, so two goroutines using different ones must not interfere.
	// A shared package-level policy would make one request's retry budget another
	// request's, which is the kind of bug that only appears under load.
	var transientCalls, successCalls atomic.Int32

	transient := New(Options{MaxAttempts: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond, Rand: deterministic(0)})
	stable := New(Options{MaxAttempts: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond, Rand: deterministic(0)})

	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = transient.Do(context.Background(), func(context.Context) error {
				transientCalls.Add(1)
				return errTransient
			})
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			_ = stable.Do(context.Background(), func(context.Context) error {
				successCalls.Add(1)
				return nil
			})
		}()
	}
	for range 16 {
		<-done
	}

	if got := transientCalls.Load(); got != 16 {
		t.Errorf("the transient policy ran %d times, want 8 x MaxAttempts(2)", got)
	}
	// A successful call must still make exactly one attempt, no matter how many
	// goroutines share the policy value.
	if got := successCalls.Load(); got != 8 {
		t.Errorf("the stable policy ran %d times, want 8", got)
	}
}
