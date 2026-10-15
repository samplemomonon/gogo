package di

import "sync"

// Lifecycle collects cleanup callbacks acquired while assembling an object
// graph — connection closes, worker stops, subscription cancels — and runs them
// once, in reverse registration order, on Close. It gives a composition root a
// single shutdown hook no matter how many providers acquired a resource, and
// reverse order releases a dependent resource before the dependency it was
// built on.
type Lifecycle struct {
	mu   sync.Mutex
	fns  []func()
	done bool
}

// NewLifecycle returns an empty lifecycle.
func NewLifecycle() *Lifecycle { return &Lifecycle{} }

// Add registers a cleanup callback. A nil callback is ignored, and an Add after
// Close is a no-op: the lifecycle has already drained and will not run again.
func (l *Lifecycle) Add(fn func()) {
	if fn == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.fns = append(l.fns, fn)
}

// Close runs every registered callback in reverse registration order. It is
// idempotent: the first call drains the callbacks, and every later call is a
// no-op.
func (l *Lifecycle) Close() {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	fns := l.fns
	l.fns = nil
	l.mu.Unlock()

	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
}
