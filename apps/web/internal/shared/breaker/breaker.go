// Package breaker provides a small, dependency-free circuit breaker for
// outbound calls. It protects a downstream service (here, the remote catalog)
// from cascading failure: after a run of consecutive failures the circuit
// opens and rejects calls immediately with a fast error, then lets a limited
// number of probe calls through after a cooldown to test recovery. Business
// rejections (a clean answer like "not found") never trip the circuit — only
// transport-level failures do — so a healthy service answering 404s correctly
// is never treated as down.
package breaker

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// State is the circuit's current condition.
type State int

const (
	// StateClosed lets every call through while failures stay under the trip
	// threshold.
	StateClosed State = iota
	// StateOpen rejects calls immediately until the cooldown elapses.
	StateOpen
	// StateHalfOpen admits a bounded number of probe calls to test recovery.
	StateHalfOpen
)

// ErrOpen is returned by Execute while the circuit is open (or its probe
// capacity is exhausted), signalling "downstream is unavailable".
var ErrOpen = errors.New("breaker: circuit open")

// DefaultConfig returns a breaker configuration suitable for most remote
// service calls: trip after 5 consecutive failures, wait 5 seconds before
// probing, allow 1 success to close the circuit.
func DefaultConfig() (threshold int, timeout time.Duration, successesToClose int) {
	return 5, 5 * time.Second, 1
}

// NewDefault creates a breaker with the default configuration.
func NewDefault() *Breaker {
	threshold, timeout, successesToClose := DefaultConfig()
	return New(threshold, timeout, successesToClose)
}

// Breaker guards an outbound call site against cascading failure.
//
//   - Closed: all calls allowed; consecutive transport failures count up; at
//     maxFailures the circuit trips to Open.
//   - Open: for the cooldown, calls are rejected immediately with ErrOpen.
//     After cooldown it moves to HalfOpen.
//   - HalfOpen: at most maxProbes concurrent calls are let through as probes;
//     a success closes the circuit, a failure reopens it for another cooldown.
//
// A Breaker is safe for concurrent use.
type Breaker struct {
	mu          sync.Mutex
	state       State
	failures    int
	openedAt    time.Time
	halfOpen    int // outstanding probe calls
	maxFailures int
	cooldown    time.Duration
	maxProbes   int

	// Counters, aggregated for observability.
	counts struct {
		calls   atomic.Uint64
		success atomic.Uint64
		failure atomic.Uint64
		trips   atomic.Uint64
	}
}

// New returns a circuit breaker that trips after maxFailures consecutive
// failures, stays open at least cooldown, and admits maxProbes concurrent
// probe calls when half-open. Non-positive inputs fall back to sensible
// defaults.
func New(maxFailures int, cooldown time.Duration, maxProbes int) *Breaker {
	if maxFailures <= 0 {
		maxFailures = 5
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	if maxProbes <= 0 {
		maxProbes = 1
	}
	return &Breaker{
		state:       StateClosed,
		maxFailures: maxFailures,
		cooldown:    cooldown,
		maxProbes:   maxProbes,
	}
}

// Execute runs fn under the circuit. fn must return nil when the call
// succeeded — including when the downstream answered cleanly with a business
// rejection such as "not found" — and a non-nil error only for a
// transport-level failure. A non-nil return is treated as a circuit failure.
// While open (or with probe capacity exhausted), Execute returns ErrOpen
// without calling fn.
func (b *Breaker) Execute(fn func() error) error {
	b.counts.calls.Add(1)

	if !b.allow() {
		b.counts.failure.Add(1)
		return ErrOpen
	}

	err := fn()
	if err == nil {
		b.recordSuccess()
		b.counts.success.Add(1)
		return nil
	}

	b.recordFailure()
	b.counts.failure.Add(1)
	return err
}

// allow decides whether a call may proceed now, transitioning state as
// appropriate. Callers hold no lock on entry.
func (b *Breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(b.openedAt) < b.cooldown {
			return false
		}
		// Cooldown elapsed: admit a probe.
		b.state = StateHalfOpen
		b.halfOpen = 1
		return true
	case StateHalfOpen:
		if b.halfOpen >= b.maxProbes {
			return false
		}
		b.halfOpen++
		return true
	default:
		return true
	}
}

// recordSuccess is called after a successful call.
func (b *Breaker) recordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		// A probe succeeded: the downstream is healthy again, close.
		b.openLocked(StateClosed)
		b.failures = 0
	case StateClosed:
		b.failures = 0
	}
}

// recordFailure is called after a failed call and advances the state machine.
func (b *Breaker) recordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		// A probe failed: reopen the circuit for a fresh cooldown.
		b.openLocked(StateOpen)
	case StateClosed:
		b.failures++
		if b.failures >= b.maxFailures {
			b.openLocked(StateOpen)
		}
	}
}

// openLocked moves the circuit to the given open-like state, stamping the
// open time and clearing probe accounting. Callers hold the lock.
func (b *Breaker) openLocked(to State) {
	if b.state != StateOpen {
		b.openedAt = time.Now()
	}
	b.state = to
	b.halfOpen = 0
	b.counts.trips.Add(1)
}

// State reports the circuit's current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Counters returns the aggregated call/success/failure/trip totals.
func (b *Breaker) Counters() (calls, success, failure, trips uint64) {
	return b.counts.calls.Load(),
		b.counts.success.Load(),
		b.counts.failure.Load(),
		b.counts.trips.Load()
}
