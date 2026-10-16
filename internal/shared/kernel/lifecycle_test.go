package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// service is a test double that records its own lifecycle calls into a shared
// trace, so ordering is asserted rather than assumed.
type service struct {
	name     string
	trace    *[]string
	startErr error
	stopErr  error
}

func (s *service) Start(context.Context) error {
	*s.trace = append(*s.trace, "start:"+s.name)
	return s.startErr
}

func (s *service) Stop(context.Context) error {
	*s.trace = append(*s.trace, "stop:"+s.name)
	return s.stopErr
}

func TestServiceManagerStartAll(t *testing.T) {
	t.Parallel()

	t.Run("starts services in registration order", func(t *testing.T) {
		t.Parallel()

		var trace []string
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace})
		m.Register(&service{name: "b", trace: &trace})

		if err := m.StartAll(context.Background()); err != nil {
			t.Fatalf("StartAll: %v", err)
		}
		if got, want := strings.Join(trace, ","), "start:a,start:b"; got != want {
			t.Errorf("trace = %q, want %q", got, want)
		}
	})

	t.Run("refuses a second StartAll", func(t *testing.T) {
		t.Parallel()

		var trace []string
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace})

		if err := m.StartAll(context.Background()); err != nil {
			t.Fatalf("first StartAll: %v", err)
		}
		if err := m.StartAll(context.Background()); err == nil {
			t.Fatal("second StartAll succeeded, want an error")
		}
		// The double start must not have started anything a second time.
		if got, want := strings.Join(trace, ","), "start:a"; got != want {
			t.Errorf("trace = %q, want %q", got, want)
		}
	})

	t.Run("rolls back already-started services when one fails", func(t *testing.T) {
		t.Parallel()

		var trace []string
		boom := errors.New("port already in use")
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace})
		m.Register(&service{name: "b", trace: &trace})
		m.Register(&service{name: "c", trace: &trace, startErr: boom})
		m.Register(&service{name: "d", trace: &trace})

		err := m.StartAll(context.Background())
		if !errors.Is(err, boom) {
			t.Fatalf("StartAll = %v, want the failing service's error", err)
		}
		// a and b started; c failed; d was never attempted; b then a were
		// stopped in reverse — a half-started manager must not leak servers.
		want := "start:a,start:b,start:c,stop:b,stop:a"
		if got := strings.Join(trace, ","); got != want {
			t.Errorf("trace = %q, want %q", got, want)
		}

		// The rollback leaves the manager un-started, so StopAll is a no-op
		// rather than a second shutdown of services the rollback already
		// stopped.
		if err := m.StopAll(context.Background()); err != nil {
			t.Fatalf("StopAll after rollback: %v", err)
		}
		if got := strings.Join(trace, ","); got != want {
			t.Errorf("StopAll after rollback changed the trace to %q", got)
		}
	})

	t.Run("an empty manager starts cleanly", func(t *testing.T) {
		t.Parallel()

		if err := NewServiceManager().StartAll(context.Background()); err != nil {
			t.Errorf("StartAll on an empty manager: %v", err)
		}
	})
}

func TestServiceManagerStopAll(t *testing.T) {
	t.Parallel()

	t.Run("stops services in reverse order", func(t *testing.T) {
		t.Parallel()

		var trace []string
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace})
		m.Register(&service{name: "b", trace: &trace})
		if err := m.StartAll(context.Background()); err != nil {
			t.Fatalf("StartAll: %v", err)
		}

		trace = nil
		if err := m.StopAll(context.Background()); err != nil {
			t.Fatalf("StopAll: %v", err)
		}
		if got, want := strings.Join(trace, ","), "stop:b,stop:a"; got != want {
			t.Errorf("trace = %q, want %q", got, want)
		}
	})

	t.Run("is a no-op before StartAll", func(t *testing.T) {
		t.Parallel()

		var trace []string
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace})

		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll before start: %v", err)
		}
		if len(trace) != 0 {
			t.Errorf("StopAll before start called Stop: %v", trace)
		}
	})

	t.Run("reports every stop failure", func(t *testing.T) {
		t.Parallel()

		var trace []string
		m := NewServiceManager()
		m.Register(&service{name: "a", trace: &trace, stopErr: errors.New("a: close failed")})
		m.Register(&service{name: "b", trace: &trace, stopErr: errors.New("b: close failed")})
		if err := m.StartAll(context.Background()); err != nil {
			t.Fatalf("StartAll: %v", err)
		}

		err := m.StopAll(context.Background())
		if err == nil {
			t.Fatal("StopAll = nil, want the joined stop errors")
		}
		for _, want := range []string{"a: close failed", "b: close failed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("StopAll error %q does not mention %q; one failure must not hide the other", err, want)
			}
		}
	})
}
