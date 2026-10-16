// Package messaging_test covers the outbox recorder for derived events.
//
// The recorder's only interesting behaviour is its transaction handling: it
// opens its own transaction, inserts one event, and commits. Against a dead
// connection the first step fails, which is what this test pins; the insert
// itself is covered by the outbox store tests and the repository tests, which
// share the same outbox_events shape.
package messaging_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"product-service/internal/adapter/messaging"
	"product-service/internal/domain/product"
)

const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1&sslmode=disable"

func TestOutboxRecorderBeginFailure(t *testing.T) {
	db, err := sql.Open("postgres", unreachableDSN)
	if err != nil {
		t.Fatalf("open database handle: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	recorder := messaging.NewOutboxRecorder(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = recorder.Append(ctx, &product.DomainEvent{
		EventType: product.EventProductUpdated,
		Payload:   []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected an error from the dead connection")
	}
	if !strings.Contains(err.Error(), "begin append derived event") {
		t.Fatalf("error = %q, want it to name the begin step", err)
	}
}
