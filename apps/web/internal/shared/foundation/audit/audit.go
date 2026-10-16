// Package audit provides audit logging for compliance and security.
// Critical for e-commerce: track all changes to orders, payments, inventory, user data.
package audit

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"gogo/internal/shared/kernel"
)

// AuditLog represents an audit entry.
type AuditLog struct {
	ID           string
	Timestamp    time.Time
	UserID       string
	Action       string
	EntityType   string
	EntityID     string
	Changes      map[string]interface{}
	IPAddress    string
	UserAgent    string
	Status       string // success, failure
	ErrorMessage string
}

// Logger logs audit events.
type Logger interface {
	Log(ctx context.Context, log *AuditLog) error
	Query(ctx context.Context, filter *Filter) ([]*AuditLog, error)
}

// Filter defines query criteria for audit logs.
type Filter struct {
	UserID     string
	Action     string
	EntityType string
	EntityID   string
	StartTime  time.Time
	EndTime    time.Time
	Limit      int
}

// InMemoryLogger stores audit logs in memory.
type InMemoryLogger struct {
	mu   sync.RWMutex
	logs []*AuditLog
}

// NewInMemoryLogger creates an in-memory audit logger.
func NewInMemoryLogger() *InMemoryLogger {
	return &InMemoryLogger{
		logs: make([]*AuditLog, 0),
	}
}

// Log records an audit entry.
func (l *InMemoryLogger) Log(ctx context.Context, log *AuditLog) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if log.ID == "" {
		log.ID = generateID()
	}
	if log.Timestamp.IsZero() {
		log.Timestamp = time.Now()
	}

	l.logs = append(l.logs, log)
	return nil
}

// Query retrieves audit logs matching the filter.
func (l *InMemoryLogger) Query(ctx context.Context, filter *Filter) ([]*AuditLog, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]*AuditLog, 0)

	for _, log := range l.logs {
		if !matchesFilter(log, filter) {
			continue
		}
		result = append(result, log)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

func matchesFilter(log *AuditLog, filter *Filter) bool {
	if filter.UserID != "" && log.UserID != filter.UserID {
		return false
	}
	if filter.Action != "" && log.Action != filter.Action {
		return false
	}
	if filter.EntityType != "" && log.EntityType != filter.EntityType {
		return false
	}
	if filter.EntityID != "" && log.EntityID != filter.EntityID {
		return false
	}
	if !filter.StartTime.IsZero() && log.Timestamp.Before(filter.StartTime) {
		return false
	}
	if !filter.EndTime.IsZero() && log.Timestamp.After(filter.EndTime) {
		return false
	}
	return true
}

// Middleware wraps operations with audit logging.
type Middleware struct {
	logger Logger
}

// NewMiddleware creates audit middleware.
func NewMiddleware(logger Logger) *Middleware {
	return &Middleware{logger: logger}
}

// Wrap wraps an operation with audit logging.
func (m *Middleware) Wrap(ctx context.Context, action string, entityType string, entityID string, fn func() error) error {
	userID := extractUserID(ctx)

	log := &AuditLog{
		UserID:     userID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Status:     "success",
	}

	err := fn()
	if err != nil {
		log.Status = "failure"
		log.ErrorMessage = err.Error()
	}

	_ = m.logger.Log(ctx, log)
	return err
}

// WrapWithChanges wraps an operation and records changes.
func (m *Middleware) WrapWithChanges(ctx context.Context, action string, entityType string, entityID string, before, after interface{}, fn func() error) error {
	userID := extractUserID(ctx)

	log := &AuditLog{
		UserID:     userID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Changes:    computeChanges(before, after),
		Status:     "success",
	}

	err := fn()
	if err != nil {
		log.Status = "failure"
		log.ErrorMessage = err.Error()
	}

	_ = m.logger.Log(ctx, log)
	return err
}

func computeChanges(before, after interface{}) map[string]interface{} {
	// Simple implementation - in production use a proper diff library
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)

	var beforeMap, afterMap map[string]interface{}
	json.Unmarshal(beforeJSON, &beforeMap)
	json.Unmarshal(afterJSON, &afterMap)

	changes := make(map[string]interface{})
	for key, afterVal := range afterMap {
		beforeVal, exists := beforeMap[key]
		if !exists || beforeVal != afterVal {
			changes[key] = map[string]interface{}{
				"from": beforeVal,
				"to":   afterVal,
			}
		}
	}

	return changes
}

func extractUserID(ctx context.Context) string {
	// Extract from context - in real implementation
	if userID := ctx.Value("user_id"); userID != nil {
		if id, ok := userID.(string); ok {
			return id
		}
	}
	return "system"
}

func generateID() string {
	return time.Now().Format("20060102150405.000000")
}

// EventBusLogger publishes audit logs as events.
type EventBusLogger struct {
	eventBus kernel.EventBus
	fallback Logger
}

// NewEventBusLogger creates an audit logger that publishes events.
func NewEventBusLogger(eventBus kernel.EventBus, fallback Logger) *EventBusLogger {
	return &EventBusLogger{
		eventBus: eventBus,
		fallback: fallback,
	}
}

// Log publishes an audit event.
func (l *EventBusLogger) Log(ctx context.Context, log *AuditLog) error {
	// Publish to event bus for real-time processing
	l.eventBus.Publish(ctx, kernel.NewBaseEvent("audit.logged", log))

	// Also store in fallback for querying
	if l.fallback != nil {
		return l.fallback.Log(ctx, log)
	}

	return nil
}

// Query delegates to fallback.
func (l *EventBusLogger) Query(ctx context.Context, filter *Filter) ([]*AuditLog, error) {
	if l.fallback != nil {
		return l.fallback.Query(ctx, filter)
	}
	return nil, nil
}

// Example usage:
//
// auditLogger := audit.NewInMemoryLogger()
// middleware := audit.NewMiddleware(auditLogger)
//
// // Audit order creation
// err := middleware.Wrap(ctx, "order.create", "order", orderID, func() error {
//     return orderService.Create(ctx, order)
// })
//
// // Audit with change tracking
// err := middleware.WrapWithChanges(ctx, "order.update", "order", orderID, oldOrder, newOrder, func() error {
//     return orderService.Update(ctx, newOrder)
// })
