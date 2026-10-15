// Package breaker implements a per-dependency circuit breaker.
//
// Why an aggregator needs one: it fans out to five services, so one slow dependency
// multiplies into every request that touches it. Without a breaker, a client whose
// query touches category-service keeps several concurrent gRPC calls in flight
// against a service that is already failing, and each holds a goroutine, a connection
// slot and a share of the request budget until it times out. The breaker turns that
// into one refused call per cooldown instead.
//
// Four design choices, each for a reason that is not obvious from the code alone:
//
//   - **Only retryable failures count.** A NotFound or InvalidArgument is the service
//     working correctly. Counting those would open the breaker during a perfectly
//     healthy period in which clients happened to be sending bad requests.
//
//   - **Consecutive, not cumulative.** A single success resets the count. That is what
//     makes the breaker recover: a service that has one good call in it is not down,
//     and a breaker keyed on a running total would stay open long after the incident
//     passed.
//
//   - **Failures are counted at exactly one layer.** Every downstream call goes through
//     Execute here and nothing else counts. A breaker that also counted inside the
//     retry loop would see one user request as three failures and trip three times
//     faster than the evidence supports. This is the recommendation from the SRE
//     workbook's circuit-breaker chapter, and it is the mistake most likely to be
//     made when retry and breaker are introduced together.
//
//   - **Retry and breaker interact deliberately.** A retry loop running while the
//     breaker is half-open multiplies load on a service that is barely recovering.
//     Allow therefore refuses before any attempt is made, and the retry loop in
//     internal/retry stops when it sees an open circuit rather than retrying into it.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is a breaker's condition.
type State int

const (
	// StateClosed lets every call through. Normal operation.
	StateClosed State = iota
	// StateOpen refuses every call without attempting it. The dependency is assumed to
	// be failing, and no attempt is made until the cooldown elapses.
	StateOpen
	// StateHalfOpen admits a limited number of probes to discover whether the
	// dependency has recovered. The limit is what stops the breaker from
	// reintroducing the load it was opened to stop.
	StateHalfOpen
)

// String renders the state for logs and metrics.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// Outcome says whether a call's result should count against the breaker.
//
// It is a named type rather than a bare bool because the distinction is easy to get
// backwards at a call site: `Report(true)` reads as "report success" to anyone who
// has not read the parameter name, and getting it wrong means a breaker that opens on
// healthy traffic.
type Outcome int

const (
	// Success resets the failure run. Also used for failures that are the caller's
	// fault, because a service correctly rejecting a bad request is not a fault of
	// the service.
	Success Outcome = iota
	// Failure counts against the breaker. Use it only for a retryable failure.
	Failure
)

// ErrOpenCircuit is returned when a call is refused without being attempted.
//
// A sentinel rather than an ordinary error so callers can tell "the service is down"
// from "the service answered and said no". That distinction decides whether a retry
// is pointless: retrying a refused call helps nobody, while retrying a failed attempt
// against a recovered service might.
var ErrOpenCircuit = errors.New("circuit breaker is open")

// Defaults, shared with the rest of the platform so an operator reads one set of
// numbers rather than two.
const (
	DefaultFailureThreshold = 5
	DefaultCooldown         = 5 * time.Second
	DefaultHalfOpenProbes   = 1
)

// Breaker guards one dependency.
//
// One instance per downstream service, never shared. A shared breaker would let a
// failing media-service open the gate for category-service, which is a different
// failure with a different cause and a different recovery.
type Breaker struct {
	name string

	failureThreshold int
	cooldown         time.Duration
	halfOpenProbes   int
	probeTimeout     time.Duration
	onStateChange    func(name string, from, to State)

	mu                  sync.Mutex
	state               State
	consecutiveFailures int
	// probesInFlight counts half-open probes already admitted.
	probesInFlight int
	// openedAt is when StateOpen began, so the cooldown is measured from the
	// transition rather than from the last attempt.
	openedAt time.Time

	// pendingNotify holds transitions observed inside a lock, to be reported by the
	// public method once it can unlock. Reporting inline from a locked helper would
	// mean calling user code under a mutex.
	pendingNotify []transition

	// now is injectable so the tests do not sleep. Production leaves it nil.
	now func() time.Time
}

// notifyAll reports transitions to the callback, if one is configured.
//
// Synchronous rather than on a goroutine: the callers have already released the lock,
// so a slow callback delays only the caller that caused the transition, and a
// goroutine would make the transition log arrive out of order relative to the calls
// that produced it.
func (b *Breaker) notifyAll(pending []transition) {
	if b.onStateChange == nil {
		return
	}
	for _, t := range pending {
		b.onStateChange(b.name, t.from, t.to)
	}
}

// Options configures a Breaker.
type Options struct {
	// Name identifies the dependency in logs and errors.
	Name string
	// FailureThreshold is the number of consecutive retryable failures that trips the
	// breaker.
	FailureThreshold int
	// Cooldown is how long the breaker stays open before admitting a probe.
	Cooldown time.Duration
	// HalfOpenProbes is how many probes may run concurrently while half-open.
	HalfOpenProbes int
	// ProbeTimeout bounds a half-open probe. Zero uses a one-second default, because a
	// half-open breaker with no bound can wedge: the dependency accepts the
	// connection and then never answers.
	ProbeTimeout time.Duration
	// OnStateChange is called after every transition, never while a lock is held.
	// Optional.
	OnStateChange func(name string, from, to State)
}

// New builds a Breaker, filling any non-positive option with the platform default.
func New(opts Options) *Breaker {
	threshold := opts.FailureThreshold
	if threshold < 1 {
		threshold = DefaultFailureThreshold
	}
	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	probes := opts.HalfOpenProbes
	if probes < 1 {
		probes = DefaultHalfOpenProbes
	}
	probeTimeout := opts.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = time.Second
	}

	return &Breaker{
		name:             opts.Name,
		failureThreshold: threshold,
		cooldown:         cooldown,
		halfOpenProbes:   probes,
		probeTimeout:     probeTimeout,
		onStateChange:    opts.OnStateChange,
		state:            StateClosed,
		now:              time.Now,
	}
}

// Name is the dependency this breaker guards.
func (b *Breaker) Name() string { return b.name }

// State reports the current condition, applying the cooldown transition first.
//
// Reading the state and then acting on it would be a race, so a caller that needs to
// branch should use Allow. This is for health endpoints and metrics, where a moment of
// staleness is acceptable.
func (b *Breaker) State() State {
	b.mu.Lock()

	b.maybeHalfOpenLocked()
	state := b.state
	pending := b.drainNotifications()
	b.mu.Unlock()

	b.notifyAll(pending)
	return state
}

// Allow reserves capacity for one call and reports whether it may proceed.
//
// Every path that attempts a downstream call goes through here, including the first
// attempt of a retry loop. A breaker that only gated retries would still let the
// initial call through, which is the one call it exists to avoid.
func (b *Breaker) Allow() error {
	b.mu.Lock()

	b.maybeHalfOpenLocked()

	var err error
	switch b.state {
	case StateClosed:
		// allowed

	case StateHalfOpen:
		if b.probesInFlight >= b.halfOpenProbes {
			// The probe budget is spent. Refusing here is what keeps half-open from
			// becoming open with extra steps and extra logging.
			err = fmt.Errorf("%w: %s is half-open and all %d probe slots are in use",
				ErrOpenCircuit, b.name, b.halfOpenProbes)
			break
		}
		b.probesInFlight++

	default: // StateOpen
		err = fmt.Errorf("%w: %s failed %d times consecutively, cooling down for %s",
			ErrOpenCircuit, b.name, b.consecutiveFailures, b.cooldown)
	}

	pending := b.drainNotifications()
	b.mu.Unlock()

	b.notifyAll(pending)
	return err
}

// Report records the outcome of a call that Allow admitted.
//
// A half-open probe's Report also releases the probe slot, so a probe that panics or
// whose context is cancelled still returns the breaker to a settled state.
func (b *Breaker) Report(outcome Outcome) {
	b.mu.Lock()

	from := b.state

	switch {
	case from == StateHalfOpen:
		if b.probesInFlight > 0 {
			b.probesInFlight--
		}
		if outcome == Failure {
			// A failed probe reopens immediately rather than waiting out another
			// full run of failures: the probe exists precisely to test recovery, and
			// its failure is the answer.
			b.consecutiveFailures = b.failureThreshold
			b.setStateLocked(StateOpen)
		} else {
			b.setStateLocked(StateClosed)
		}

	case outcome == Failure:
		if from == StateClosed {
			// Only a closed breaker accumulates. Incrementing in half-open as well
			// would double-count the failure that just reopened it.
			b.consecutiveFailures++
			if b.consecutiveFailures >= b.failureThreshold {
				b.setStateLocked(StateOpen)
			}
		}

	default:
		// A success in the closed state clears the run, which is what lets the breaker
		// recover from a brief blip without waiting out a cooldown.
		b.consecutiveFailures = 0
	}

	var pending []transition
	if b.state != from {
		pending = append(pending, transition{from: from, to: b.state})
	}
	pending = append(pending, b.drainNotifications()...)
	b.mu.Unlock()

	// The callback runs with no lock held, so a slow log write cannot block another
	// request's Allow. The transition is already applied, so a late callback still
	// reports the truth.
	b.notifyAll(pending)
}

// Execute runs fn under the breaker: reserve, run, report.
//
// This is the form the gRPC clients use. The two-step Allow/Report split exists for
// the retry loop, which reserves once and reports each attempt.
func (b *Breaker) Execute(ctx context.Context, outcome func(error) Outcome, fn func(context.Context) error) error {
	if err := b.Allow(); err != nil {
		return err
	}

	err := b.callWithProbeTimeout(ctx, fn)
	b.Report(outcome(err))
	return err
}

// outcomeOf is a helper for the common case, where the caller has already classified
// the error and only needs to map that classification to an Outcome.
func outcomeOf(err error, retryable bool) Outcome {
	if err != nil && retryable {
		return Failure
	}
	return Success
}

// callWithProbeTimeout bounds a half-open probe.
//
// Only probes are bounded. A closed-state call is already bounded by the caller's
// per-call timeout, and a second, shorter bound here would fail calls the caller was
// willing to wait for.
func (b *Breaker) callWithProbeTimeout(ctx context.Context, fn func(context.Context) error) error {
	if b.State() != StateHalfOpen {
		return fn(ctx)
	}

	probeCtx, cancel := context.WithTimeout(ctx, b.probeTimeout)
	defer cancel()
	return fn(probeCtx)
}

// maybeHalfOpenLocked moves an expired open breaker to half-open.
//
// The caller holds the lock.
func (b *Breaker) maybeHalfOpenLocked() {
	if b.state != StateOpen {
		return
	}
	if b.now().Sub(b.openedAt) < b.cooldown {
		return
	}

	from := b.state
	b.setStateLocked(StateHalfOpen)
	b.pendingNotify = append(b.pendingNotify, transition{from: from, to: StateHalfOpen})
}

// transition is a recorded state change awaiting notification.
type transition struct {
	from, to State
}

// setStateLocked applies a state change and its bookkeeping.
//
// The caller holds the lock.
func (b *Breaker) setStateLocked(to State) {
	if b.state == to {
		return
	}
	b.state = to

	switch to {
	case StateOpen:
		b.openedAt = b.now()
		// A breaker that just reopened has no probes in flight by definition: the one
		// that failed was the probe. Resetting makes that explicit rather than
		// leaving a stale count to leak into the next half-open window.
		b.probesInFlight = 0
	case StateHalfOpen, StateClosed:
		b.probesInFlight = 0
	}
	if to == StateClosed {
		b.consecutiveFailures = 0
	}
}

// drainNotifications returns and clears the queued transitions.
//
// Called with the lock held by the public methods that can observe one.
func (b *Breaker) drainNotifications() []transition {
	if len(b.pendingNotify) == 0 {
		return nil
	}
	pending := b.pendingNotify
	b.pendingNotify = nil
	return pending
}

// Snapshot returns the breaker's internal counters, for tests and a debug endpoint.
func (b *Breaker) Snapshot() (state State, consecutiveFailures, probesInFlight int) {
	b.mu.Lock()

	b.maybeHalfOpenLocked()
	state, failures, inFlight := b.state, b.consecutiveFailures, b.probesInFlight
	pending := b.drainNotifications()
	b.mu.Unlock()

	b.notifyAll(pending)
	return state, failures, inFlight
}
