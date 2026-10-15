// Package retry re-issues a failed downstream call, within a budget the caller's
// context already sets.
//
// The design is driven by one fact: the caller gave us a deadline. That deadline is
// the real constraint, not a retry count. A retry that runs past it is not a retry,
// it is latency added to a request that has already timed out, and it holds the
// request's goroutine and connection for the duration. So every wait here is bounded
// by the time actually remaining, and a retry that cannot fit is not attempted.
//
// Four rules that are easy to get wrong:
//
//   - **Retry only what is safe to repeat.** A retried CreateProduct can create a
//     second product. Every call site names its idempotency explicitly rather than
//     relying on a default, so adding an unsafe call is a visible act.
//
//   - **Stop on a non-retryable error immediately.** Retrying InvalidArgument gets the
//     same answer and wastes the caller's remaining budget. The classification comes
//     from the error vocabulary in internal/apperror, which is also what the breaker
//     counts on, so the two cannot disagree about which failures are transient.
//
//   - **Full jitter, not fixed backoff.** Without jitter, every client that failed
//     together retries together, and the retry wave is the outage. jitter picks a
//     uniform point in the window rather than always the top of it.
//
//   - **Never retry into an open circuit.** If the breaker says the dependency is
//     down, sleeping and trying again sends load at a service that has already been
//     judged unable to take it. That is the interaction the SRE workbook warns about:
//     a half-open breaker plus retries amplifies exactly when the service is weakest.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
)

// Options configures a retry policy.
type Options struct {
	// MaxAttempts is the total number of attempts, including the first. Two means one
	// retry.
	MaxAttempts int
	// BaseDelay is the first backoff window. Each subsequent window doubles.
	BaseDelay time.Duration
	// MaxDelay caps a single backoff window.
	MaxDelay time.Duration
	// Rand supplies the jitter. Nil uses the global source. Injectable so tests can
	// make the schedule deterministic.
	Rand func() float64
	// OnRetry is called before each retry, for logging and for span events. Optional.
	OnRetry func(attempt int, delay time.Duration, err error)
}

// Policy is a retry policy bound to a caller.
//
// It is a value rather than a package-level function so the attempt budget and the
// backoff schedule are explicit at the call site instead of being hidden in a global.
type Policy struct {
	options Options
}

// New builds a Policy, filling non-positive options with defaults.
func New(opts Options) Policy {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 2
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = 50 * time.Millisecond
	}
	if opts.MaxDelay <= 0 {
		// One second. Longer than any useful backoff for a synchronous request that
		// already has a ten-second budget, and the ceiling is what stops a doubling
		// schedule from eating the whole budget in three attempts.
		opts.MaxDelay = time.Second
	}
	if opts.Rand == nil {
		opts.Rand = rand.Float64
	}
	return Policy{options: opts}
}

// Read is the policy for idempotent operations.
//
// Reads are safe to repeat: they change nothing, so a second attempt either returns
// the same answer or a fresher one. This is the default for anything whose only effect
// is to read.
func Read() Policy {
	return New(Options{MaxAttempts: 3, BaseDelay: 40 * time.Millisecond, MaxDelay: 400 * time.Millisecond})
}

// Sensitive is the policy for operations that are not safe to repeat.
//
// One attempt, no retries. Used for writes, because product-service has no
// idempotency key on CreateProduct: a repeated call creates a second product, and the
// resulting duplicate is far more expensive to clean up than a failed request is to
// retry.
func Sensitive() Policy {
	return New(Options{MaxAttempts: 1})
}

// MaxAttempts reports the configured attempt budget, for logging.
func (p Policy) MaxAttempts() int {
	if p.options.MaxAttempts < 1 {
		return 1
	}
	return p.options.MaxAttempts
}

// Do runs fn until it succeeds, fails non-retryably, or runs out of budget.
//
// fn is called with a context that will be cancelled no later than the caller's, so
// the per-attempt timeout in the gRPC client applies to each attempt individually.
func (p Policy) Do(ctx context.Context, fn func(context.Context) error) error {
	// A zero Policy is the struct's default, so Do has to be safe when given one.
	// Normalising here rather than in New means a caller who composes a Policy by hand
	// gets a single attempt, which is the conservative reading, instead of a loop that
	// never runs and reports success without ever calling fn.
	attempts := p.options.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		// Checked before every attempt, including the first. A caller whose budget is
		// already spent should not get one attempt plus a retry; it should get nothing.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				// The budget expired mid-retry. Reporting the underlying failure is
				// more useful than reporting only the deadline, because it says what
				// was actually wrong with the dependency.
				return lastErr
			}
			return err
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if attempt == attempts || !Retryable(ctx, lastErr) {
			return lastErr
		}

		delay, ok := p.waitFor(ctx, attempt, lastErr)
		if !ok {
			// The next attempt would not fit inside the caller's deadline. Returning
			// the real error beats returning a deadline error that hides it.
			return lastErr
		}
		// Waiting is interruptible by the caller's cancellation, so a request that
		// gives up does not sit in a timer until the delay elapses.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastErr
		case <-timer.C:
		}
	}

	return lastErr
}

// waitFor computes the backoff for the next attempt and reports whether it fits.
//
// Returns false when the wait would exceed the remaining budget, which is the case
// that stops a retry from extending a request past its deadline.
func (p Policy) waitFor(ctx context.Context, attempt int, err error) (time.Duration, bool) {
	delay := p.backoff(attempt)

	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		// The attempt itself needs budget too, so the wait is capped at half of what
		// is left. Reserving half rather than requiring delay < remaining means a
		// retry is not started when it would leave the call no time to run in.
		if remaining <= delay*2 {
			return 0, false
		}
		delay = min(delay, remaining/2)
	}

	if p.options.OnRetry != nil {
		p.options.OnRetry(attempt, delay, err)
	}
	return delay, true
}

// backoff computes the jittered delay before the given attempt's successor.
//
// The window doubles per attempt and is then sampled uniformly. Sampling the whole
// window rather than always taking the top of it is what spreads a retry wave: without
// jitter, every request that failed at the same instant retries at the same instant,
// which reproduces the failure it is recovering from.
func (p Policy) backoff(attempt int) time.Duration {
	window := p.options.BaseDelay
	// Shifting by attempt-1 keeps the first retry at the base delay. The shift is
	// bounded so a large MaxAttempts cannot overflow the duration into a negative
	// value, which would produce a negative delay and a busy retry loop.
	if attempt > 1 && attempt-1 < 62 {
		window <<= uint(attempt - 1)
	}
	if window > p.options.MaxDelay || window <= 0 {
		window = p.options.MaxDelay
	}

	if p.options.Rand == nil {
		return window
	}
	// Float64 returns [0,1). Multiplying rather than dividing keeps the result a
	// duration without a conversion that could round up past the window.
	sample := p.options.Rand()
	if sample < 0 {
		sample = 0
	}
	if sample > 1 {
		sample = 1
	}
	return time.Duration(float64(window) * sample)
}

// Retryable reports whether an error is worth another attempt, given that the caller's
// context is still alive.
//
// The context argument is what makes a deadline answerable. A per-attempt timeout and
// an exhausted request budget both surface as context.DeadlineExceeded, and they mean
// opposite things: the first is a slow dependency worth retrying, the second means
// there is no time left to retry in. Only the caller can tell them apart, so the
// caller is asked.
//
// An open circuit is explicitly not retryable. Sleeping and trying again against a
// dependency the breaker has already judged unable to take traffic is precisely the
// amplification the two mechanisms are meant to prevent together.
func Retryable(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, breaker.ErrOpenCircuit) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		// The caller went away. There is nobody left to answer.
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		// The request budget is spent. Whatever the error says, there is no budget to
		// retry within, so this is the end regardless of classification.
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// The attempt's own deadline expired while the request still had budget left.
		return true
	}
	return errors.Is(err, apperror.ErrUnavailable)
}

// Outcome maps a call's error to the breaker's vocabulary.
//
// It lives here rather than in internal/breaker so the retry policy and the breaker
// are guaranteed to agree about which failures are transient. Two copies of this
// predicate would eventually drift, and the drift would show up as a breaker that
// opens on traffic the retry loop considers hopeless.
func Outcome(ctx context.Context, err error) breaker.Outcome {
	if err != nil && Retryable(ctx, err) {
		return breaker.Failure
	}
	return breaker.Success
}
