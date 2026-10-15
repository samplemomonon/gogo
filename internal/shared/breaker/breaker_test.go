package breaker

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	b := New(3, time.Hour, 1)

	for i := 0; i < 3; i++ {
		if err := b.Execute(func() error { return errors.New("boom") }); err == nil {
			t.Fatalf("call %d should have failed", i)
		}
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want StateOpen after 3 failures", b.State())
	}

	// While open, calls are rejected with ErrOpen and fn is not invoked.
	calls := 0
	err := b.Execute(func() error { calls++; return nil })
	if !errors.Is(err, ErrOpen) {
		t.Errorf("open call error = %v, want ErrOpen", err)
	}
	if calls != 0 {
		t.Errorf("fn ran %d times while open, want 0", calls)
	}
}

func TestBreakerOpensAfterCooldownAndClosesOnProbeSuccess(t *testing.T) {
	b := New(1, 20*time.Millisecond, 1)

	// Trip the circuit with a single failure.
	_ = b.Execute(func() error { return errors.New("boom") })
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want StateOpen", b.State())
	}

	// Still open within the cooldown: rejected.
	if err := b.Execute(func() error { return nil }); !errors.Is(err, ErrOpen) {
		t.Errorf("within cooldown error = %v, want ErrOpen", err)
	}

	// After the cooldown, a probe is admitted; its success closes the circuit.
	time.Sleep(25 * time.Millisecond)
	if err := b.Execute(func() error { return nil }); err != nil {
		t.Fatalf("half-open probe returned error: %v", err)
	}
	if b.State() != StateClosed {
		t.Errorf("state = %v, want StateClosed after successful probe", b.State())
	}
}

func TestBreakerReopensWhenProbeFails(t *testing.T) {
	b := New(1, 20*time.Millisecond, 1)

	_ = b.Execute(func() error { return errors.New("boom") }) // open

	time.Sleep(25 * time.Millisecond)
	// The half-open probe fails: the circuit must reopen.
	_ = b.Execute(func() error { return errors.New("still down") })
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want StateOpen after failed probe", b.State())
	}

	// And it rejects calls again until the next cooldown.
	if err := b.Execute(func() error { return nil }); !errors.Is(err, ErrOpen) {
		t.Errorf("reopened circuit error = %v, want ErrOpen", err)
	}
}

func TestBreakerLimitsHalfOpenProbes(t *testing.T) {
	b := New(1, time.Hour, 2)

	// Open the circuit, then jump past the cooldown so allow() transitions to
	// half-open.
	b.Execute(func() error { return errors.New("boom") })
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want StateOpen", b.State())
	}
	b.openedAt = time.Now().Add(-2 * time.Hour)

	// The first allow() admits a probe and moves to half-open; with a probe
	// capacity of 2, the next two calls are admitted, then further ones are
	// rejected without invoking fn.
	if !b.allow() {
		t.Error("first half-open call should be admitted")
	}
	if !b.allow() {
		t.Error("second half-open call should be admitted")
	}
	if b.allow() {
		t.Error("third half-open call should be rejected (capacity 2)")
	}
	if b.allow() {
		t.Error("fourth half-open call should be rejected (capacity 2)")
	}
}

func TestBreakerCounters(t *testing.T) {
	b := New(2, time.Hour, 1)

	_ = b.Execute(func() error { return errors.New("f") }) // failure
	b.Execute(func() error { return nil })                 // success
	_ = b.Execute(func() error { return errors.New("f") }) // failure

	calls, success, failure, _ := b.Counters()
	if calls != 3 || success != 1 || failure != 2 {
		t.Errorf("counters = calls %d, success %d, failure %d; want 3, 1, 2",
			calls, success, failure)
	}
}
