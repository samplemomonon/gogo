// Package adapter provides reusable adapter patterns for bridging between
// existing code and the new kernel interfaces.
package adapter

import (
	"context"
	"log/slog"

	"gogo/internal/shared/events"
	"gogo/internal/shared/kernel"
)

// EventBusAdapter adapts the existing events.Bus to kernel.EventBus.
type EventBusAdapter struct {
	bus events.TypedBus
}

// NewEventBusAdapter creates an adapter for the existing event bus.
func NewEventBusAdapter(bus events.TypedBus) *EventBusAdapter {
	return &EventBusAdapter{bus: bus}
}

// Publish adapts kernel.Event to events.Event.
func (a *EventBusAdapter) Publish(ctx context.Context, event kernel.Event) {
	a.bus.Publish(ctx, events.Event{
		Type:    event.Type(),
		Payload: event.Payload(),
	})
}

// Subscribe adapts kernel.EventHandler to events.Handler.
func (a *EventBusAdapter) Subscribe(ctx context.Context, handler kernel.EventHandler) (kernel.Subscription, error) {
	// Use SubscribeTypes with no types to get all events (equivalent to Subscribe)
	// This gives us a cancellable subscription
	sub := a.bus.SubscribeTypes(func(ctx context.Context, event events.Event) {
		kernelEvent := kernel.NewBaseEvent(event.Type, event.Payload)
		_ = handler(ctx, kernelEvent)
	})

	return &subscriptionAdapter{sub: sub}, nil
}

// SubscribeTypes subscribes to specific event types.
func (a *EventBusAdapter) SubscribeTypes(ctx context.Context, handler kernel.EventHandler, types ...string) (kernel.Subscription, error) {
	sub := a.bus.SubscribeTypes(func(ctx context.Context, event events.Event) {
		kernelEvent := kernel.NewBaseEvent(event.Type, event.Payload)
		_ = handler(ctx, kernelEvent)
	}, types...)

	return &subscriptionAdapter{sub: sub}, nil
}

// subscriptionAdapter adapts events subscription to kernel.Subscription.
type subscriptionAdapter struct {
	sub *events.Subscription
}

func (s *subscriptionAdapter) Unsubscribe() error {
	if s.sub != nil {
		s.sub.Cancel()
	}
	return nil
}

// Ensure EventBusAdapter implements kernel.EventBus.
var _ kernel.EventBus = (*EventBusAdapter)(nil)

// LoggerAdapter adapts slog.Logger to kernel.Logger.
type LoggerAdapter struct {
	logger *slog.Logger
}

// NewLoggerAdapter creates a new logger adapter.
func NewLoggerAdapter(logger *slog.Logger) *LoggerAdapter {
	return &LoggerAdapter{logger: logger}
}

func (l *LoggerAdapter) Debug(msg string, fields ...interface{}) {
	l.logger.Debug(msg, fields...)
}

func (l *LoggerAdapter) Info(msg string, fields ...interface{}) {
	l.logger.Info(msg, fields...)
}

func (l *LoggerAdapter) Warn(msg string, fields ...interface{}) {
	l.logger.Warn(msg, fields...)
}

func (l *LoggerAdapter) Error(msg string, fields ...interface{}) {
	l.logger.Error(msg, fields...)
}

func (l *LoggerAdapter) With(fields ...interface{}) kernel.Logger {
	return &LoggerAdapter{logger: l.logger.With(fields...)}
}

// Ensure LoggerAdapter implements kernel.Logger.
var _ kernel.Logger = (*LoggerAdapter)(nil)
