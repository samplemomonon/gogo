package events_test

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gogo/internal/shared/events"
)

func TestAsyncBus_PublishAsync(t *testing.T) {
	bus := events.NewMemoryBus()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := events.DefaultAsyncConfig()
	cfg.Workers = 2
	cfg.BufferSize = 10

	asyncBus := events.NewAsyncBus(bus, logger, cfg)
	defer asyncBus.Close()

	var received atomic.Int32
	bus.Subscribe(func(ctx context.Context, event events.Event) {
		received.Add(1)
	})

	// Publish async events
	for i := 0; i < 5; i++ {
		err := asyncBus.PublishAsync(events.Event{
			Type:    "test.event",
			Payload: i,
		})
		if err != nil {
			t.Fatalf("PublishAsync failed: %v", err)
		}
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	if received.Load() != 5 {
		t.Errorf("expected 5 events received, got %d", received.Load())
	}
}

func TestAsyncBus_Retry(t *testing.T) {
	bus := events.NewMemoryBus()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := events.AsyncConfig{
		Workers:      1,
		BufferSize:   10,
		MaxRetries:   2,
		RetryBackoff: 10 * time.Millisecond,
	}

	asyncBus := events.NewAsyncBus(bus, logger, cfg)
	defer asyncBus.Close()

	var attempts atomic.Int32
	bus.Subscribe(func(ctx context.Context, event events.Event) {
		count := attempts.Add(1)
		if count < 3 {
			panic("simulated failure")
		}
	})

	err := asyncBus.PublishAsync(events.Event{
		Type:    "test.retry",
		Payload: "data",
	})
	if err != nil {
		t.Fatalf("PublishAsync failed: %v", err)
	}

	// Wait for retries
	time.Sleep(200 * time.Millisecond)

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (1 initial + 2 retries), got %d", attempts.Load())
	}
}

func TestAsyncBus_QueueDepth(t *testing.T) {
	bus := events.NewMemoryBus()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := events.DefaultAsyncConfig()
	cfg.Workers = 0 // No workers to keep events queued
	cfg.BufferSize = 10

	asyncBus := events.NewAsyncBus(bus, logger, cfg)
	defer asyncBus.Close()

	// Publish events
	for i := 0; i < 3; i++ {
		asyncBus.PublishAsync(events.Event{Type: "test", Payload: i})
	}

	depth := asyncBus.QueueDepth()
	if depth != 3 {
		t.Errorf("expected queue depth 3, got %d", depth)
	}
}
