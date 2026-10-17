// Package audit provides audit logging for compliance and security.
// Critical for e-commerce: track all changes to orders, payments, inventory, user data.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"

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
	if err := ctx.Err(); err != nil {
		return err
	}
	if log == nil {
		return errors.New("audit log is nil")
	}
	entry, err := prepareAuditLog(log)
	if err != nil {
		return fmt.Errorf("prepare audit log: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	l.logs = append(l.logs, entry)
	return nil
}

// Query retrieves audit logs matching the filter.
func (l *InMemoryLogger) Query(ctx context.Context, filter *Filter) ([]*AuditLog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter == nil {
		filter = &Filter{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]*AuditLog, 0)

	for _, log := range l.logs {
		if !matchesFilter(log, filter) {
			continue
		}
		entry, err := cloneAuditLog(log)
		if err != nil {
			return nil, fmt.Errorf("clone audit result: %w", err)
		}
		result = append(result, entry)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

func cloneAuditLog(log *AuditLog) (*AuditLog, error) {
	copy := *log
	if log.Changes != nil {
		encoded, err := json.Marshal(log.Changes)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &copy.Changes); err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func prepareAuditLog(log *AuditLog) (*AuditLog, error) {
	if log == nil {
		return nil, errors.New("audit log is nil")
	}
	entry, err := cloneAuditLog(log)
	if err != nil {
		return nil, err
	}
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	return entry, nil
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
	if m == nil || m.logger == nil {
		return errors.New("audit logger is not configured")
	}
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

	return errors.Join(err, m.logger.Log(ctx, log))
}

// WrapWithChanges wraps an operation and records changes.
func (m *Middleware) WrapWithChanges(ctx context.Context, action string, entityType string, entityID string, before, after interface{}, fn func() error) error {
	if m == nil || m.logger == nil {
		return errors.New("audit logger is not configured")
	}
	userID := extractUserID(ctx)
	changes, changeErr := computeChanges(before, after)
	if changeErr != nil {
		return changeErr
	}

	log := &AuditLog{
		UserID:     userID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Changes:    changes,
		Status:     "success",
	}

	err := fn()
	if err != nil {
		log.Status = "failure"
		log.ErrorMessage = err.Error()
	}

	return errors.Join(err, m.logger.Log(ctx, log))
}

func computeChanges(before, after interface{}) (map[string]interface{}, error) {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return nil, fmt.Errorf("marshal before audit value: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return nil, fmt.Errorf("marshal after audit value: %w", err)
	}

	var beforeMap, afterMap map[string]interface{}
	if err := json.Unmarshal(beforeJSON, &beforeMap); err != nil {
		return nil, fmt.Errorf("decode before audit value: %w", err)
	}
	if err := json.Unmarshal(afterJSON, &afterMap); err != nil {
		return nil, fmt.Errorf("decode after audit value: %w", err)
	}

	changes := make(map[string]interface{})
	for key, afterVal := range afterMap {
		beforeVal, exists := beforeMap[key]
		if !exists || !reflect.DeepEqual(beforeVal, afterVal) {
			changes[key] = map[string]interface{}{
				"from": beforeVal,
				"to":   afterVal,
			}
		}
	}
	for key, beforeVal := range beforeMap {
		if _, exists := afterMap[key]; !exists {
			changes[key] = map[string]interface{}{"from": beforeVal, "to": nil}
		}
	}

	return changes, nil
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
	if l == nil || l.eventBus == nil {
		return errors.New("audit event bus is not configured")
	}
	if log == nil {
		return errors.New("audit log is nil")
	}
	entry, err := prepareAuditLog(log)
	if err != nil {
		return fmt.Errorf("prepare audit log: %w", err)
	}
	// Publish to event bus for real-time processing
	l.eventBus.Publish(ctx, kernel.NewBaseEvent("audit.logged", entry))

	// Also store in fallback for querying
	if l.fallback != nil {
		return l.fallback.Log(ctx, entry)
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
