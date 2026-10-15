// Package events provides async event processing with worker pools.
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// AsyncBus wraps a Bus with asynchronous event processing using a worker pool.
// Events are queued and processed in the background with configurable retry logic.
type AsyncBus struct {
	bus       *MemoryBus
	logger    *slog.Logger
	queue     chan Event
	workers   int
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	retryMax  int
	retryWait time.Duration

	// dispatchMu serializes processWithRetry. Panic attribution is installed
	// as a reporter on the shared bus, so the install/publish/restore window
	// must exclude other workers: a reporter is bus-wide, not per-publish, and
	// without this lock concurrent workers both mutate it and a panic is
	// attributed to the wrong attempt.
	dispatchMu sync.Mutex
}

// AsyncConfig configures async event processing.
type AsyncConfig struct {
	Workers      int           // Number of worker goroutines
	BufferSize   int           // Event queue buffer size
	MaxRetries   int           // Maximum retry attempts on error
	RetryBackoff time.Duration // Wait between retries
}

// DefaultAsyncConfig provides sensible defaults.
func DefaultAsyncConfig() AsyncConfig {
	return AsyncConfig{
		Workers:      4,
		BufferSize:   100,
		MaxRetries:   3,
		RetryBackoff: 100 * time.Millisecond,
	}
}

// NewAsyncBus wraps a bus with async processing.
func NewAsyncBus(bus Bus, logger *slog.Logger, cfg AsyncConfig) *AsyncBus {
	ctx, cancel := context.WithCancel(context.Background())

	// We need a MemoryBus to set up panic reporting
	memBus, ok := bus.(*MemoryBus)
	if !ok {
		// If it's not a memory bus, wrap it
		memBus = NewMemoryBus()
	}

	ab := &AsyncBus{
		bus:       memBus,
		logger:    logger,
		queue:     make(chan Event, cfg.BufferSize),
		workers:   cfg.Workers,
		ctx:       ctx,
		cancel:    cancel,
		retryMax:  cfg.MaxRetries,
		retryWait: cfg.RetryBackoff,
	}

	// Start worker pool
	for i := 0; i < cfg.Workers; i++ {
		ab.wg.Add(1)
		go ab.worker()
	}

	return ab
}

// worker processes events from the queue.
func (ab *AsyncBus) worker() {
	defer ab.wg.Done()

	for {
		select {
		case <-ab.ctx.Done():
			return
		case event := <-ab.queue:
			ab.processWithRetry(event)
		}
	}
}

// processWithRetry attempts to publish an event with exponential backoff.
func (ab *AsyncBus) processWithRetry(event Event) {
	for attempt := 0; attempt <= ab.retryMax; attempt++ {
		if attempt > 0 {
			wait := ab.retryWait * time.Duration(1<<uint(attempt-1))
			time.Sleep(wait)
		}

		// Track if a panic occurred
		panicked := false
		var panicValue any

		ab.dispatchMu.Lock()

		// Set up a temporary panic reporter for this publish
		ab.bus.mu.RLock()
		oldReporter := ab.bus.onPanic
		ab.bus.mu.RUnlock()
		ab.bus.mu.Lock()
		ab.bus.onPanic = func(e Event, recovered any) {
			panicked = true
			panicValue = recovered
			if oldReporter != nil {
				oldReporter(e, recovered)
			}
		}
		ab.bus.mu.Unlock()

		// Publish the event
		ab.bus.Publish(ab.ctx, event)

		// Restore the old reporter
		ab.bus.mu.Lock()
		ab.bus.onPanic = oldReporter
		ab.bus.mu.Unlock()

		ab.dispatchMu.Unlock()

		if !panicked {
			// Success - no panic occurred
			return
		}

		// Log the failure
		ab.logger.Error("event handler panic",
			"event_type", event.Type,
			"attempt", attempt+1,
			"panic", panicValue,
		)
	}

	// Failed after all retries - log to dead letter
	ab.logger.Error("event processing failed after retries",
		"event_type", event.Type,
		"attempts", ab.retryMax+1,
	)
}

// PublishAsync queues an event for async processing.
func (ab *AsyncBus) PublishAsync(event Event) error {
	select {
	case ab.queue <- event:
		return nil
	case <-ab.ctx.Done():
		return ab.ctx.Err()
	default:
		return ErrQueueFull
	}
}

// Close stops all workers and waits for them to finish.
func (ab *AsyncBus) Close() {
	ab.cancel()
	ab.wg.Wait()
	close(ab.queue)
}

// QueueDepth returns the number of events waiting to be processed.
func (ab *AsyncBus) QueueDepth() int {
	return len(ab.queue)
}

// ErrQueueFull is returned when the event queue is at capacity.
var ErrQueueFull = &EventError{Message: "event queue full"}

// EventError represents an error in event processing.
type EventError struct {
	Message string
}

func (e *EventError) Error() string {
	return e.Message
}

func recoverToError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return &EventError{Message: "panic: " + toString(r)}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "unknown"
}
