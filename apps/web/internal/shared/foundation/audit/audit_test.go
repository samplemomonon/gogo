package audit_test

import (
	"context"
	"errors"
	"testing"

	"gogo/internal/shared/foundation/audit"
)

func TestInMemoryLogger_LogAndQuery(t *testing.T) {
	logger := audit.NewInMemoryLogger()
	ctx := context.Background()

	if err := logger.Log(ctx, &audit.AuditLog{
		UserID:     "user-1",
		Action:     "order.create",
		EntityType: "order",
		EntityID:   "order-1",
		Status:     "success",
	}); err != nil {
		t.Fatalf("failed to log: %v", err)
	}

	if err := logger.Log(ctx, &audit.AuditLog{
		UserID:     "user-2",
		Action:     "order.cancel",
		EntityType: "order",
		EntityID:   "order-2",
		Status:     "failure",
	}); err != nil {
		t.Fatalf("failed to log: %v", err)
	}

	// Query all
	all, err := logger.Query(ctx, &audit.Filter{})
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(all))
	}

	// Filter by action
	filtered, err := logger.Query(ctx, &audit.Filter{Action: "order.create"})
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 log, got %d", len(filtered))
	}
	if filtered[0].EntityID != "order-1" {
		t.Errorf("expected order-1, got %s", filtered[0].EntityID)
	}

	// Filter by user
	filtered, err = logger.Query(ctx, &audit.Filter{UserID: "user-2"})
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 log, got %d", len(filtered))
	}
}

func TestMiddleware_Wrap(t *testing.T) {
	logger := audit.NewInMemoryLogger()
	middleware := audit.NewMiddleware(logger)
	ctx := context.Background()

	err := middleware.Wrap(ctx, "order.delete", "order", "order-1", func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	logs, _ := logger.Query(ctx, &audit.Filter{})
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Status != "success" {
		t.Errorf("expected status 'success', got %s", logs[0].Status)
	}
}

func TestMiddleware_WrapWithError(t *testing.T) {
	logger := audit.NewInMemoryLogger()
	middleware := audit.NewMiddleware(logger)
	ctx := context.Background()

	expectedErr := errors.New("operation failed")
	err := middleware.Wrap(ctx, "order.delete", "order", "order-1", func() error {
		return expectedErr
	})
	if err != expectedErr {
		t.Fatalf("expected %v, got %v", expectedErr, err)
	}

	logs, _ := logger.Query(ctx, &audit.Filter{})
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Status != "failure" {
		t.Errorf("expected status 'failure', got %s", logs[0].Status)
	}
	if logs[0].ErrorMessage != "operation failed" {
		t.Errorf("expected error message, got %s", logs[0].ErrorMessage)
	}
}

func TestMiddleware_WrapWithChanges(t *testing.T) {
	logger := audit.NewInMemoryLogger()
	middleware := audit.NewMiddleware(logger)
	ctx := context.Background()

	before := map[string]interface{}{"status": "pending"}
	after := map[string]interface{}{"status": "paid"}

	err := middleware.WrapWithChanges(ctx, "order.update", "order", "order-1", before, after, func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	logs, _ := logger.Query(ctx, &audit.Filter{})
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if _, ok := logs[0].Changes["status"]; !ok {
		t.Errorf("expected 'status' change to be recorded, got %v", logs[0].Changes)
	}
}
