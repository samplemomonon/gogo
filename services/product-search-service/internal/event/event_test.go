// Package event_test pins this service's half of the event contract: which
// envelope shapes are accepted, what the payloads decode into, and — the part
// the consumer's retry logic hinges on — which failures are permanent.
package event_test

import (
	"errors"
	"strings"
	"testing"

	"product-search-service/internal/event"
)

// envelope builds a wire message with a fixed event id and the current schema
// version, so each test varies only the field it is about.
func envelope(eventType, payload string) string {
	return `{
		"event_id": "11111111-2222-3333-4444-555555555555",
		"event_type": "` + eventType + `",
		"schema_version": 1,
		"aggregate_type": "product",
		"aggregate_id": 7,
		"occurred_at": "2026-10-16T09:30:00Z",
		"payload": ` + payload + `
	}`
}

// TestDecodeAcceptsEveryKnownEventType pins the routing table: each type
// decodes into the struct the projector expects, with values carried through.
func TestDecodeAcceptsEveryKnownEventType(t *testing.T) {
	t.Parallel()

	product := `{
		"id": 7, "category_id": 3, "name": "Kaos", "slug": "kaos",
		"price_cents": 199000, "currency": "IDR", "stock": 12,
		"status": "active", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-02T00:00:00Z"
	}`

	tests := []struct {
		name      string
		eventType string
		payload   string
		check     func(t *testing.T, payload any)
	}{
		{
			name:      "product created",
			eventType: event.TypeProductCreated,
			payload:   product,
			check: func(t *testing.T, payload any) {
				snapshot, ok := payload.(*event.ProductSnapshot)
				if !ok {
					t.Fatalf("payload is %T, want *event.ProductSnapshot", payload)
				}
				if snapshot.ID != 7 || snapshot.Name != "Kaos" || snapshot.PriceCents != 199000 {
					t.Errorf("snapshot = %+v", snapshot)
				}
			},
		},
		{
			name:      "product updated",
			eventType: event.TypeProductUpdated,
			payload:   product,
			check: func(t *testing.T, payload any) {
				if _, ok := payload.(*event.ProductSnapshot); !ok {
					t.Fatalf("payload is %T, want *event.ProductSnapshot", payload)
				}
			},
		},
		{
			name:      "product deleted",
			eventType: event.TypeProductDeleted,
			payload:   `{"id": 7}`,
			check: func(t *testing.T, payload any) {
				deleted, ok := payload.(*event.ProductDeletedPayload)
				if !ok {
					t.Fatalf("payload is %T, want *event.ProductDeletedPayload", payload)
				}
				if deleted.ID != 7 {
					t.Errorf("ID = %d, want 7", deleted.ID)
				}
			},
		},
		{
			name:      "stock adjusted",
			eventType: event.TypeProductStockAdjusted,
			payload:   `{"id": 7, "stock": 10, "delta": -2}`,
			check: func(t *testing.T, payload any) {
				adjusted, ok := payload.(*event.ProductStockAdjustedPayload)
				if !ok {
					t.Fatalf("payload is %T, want *event.ProductStockAdjustedPayload", payload)
				}
				if adjusted.Stock != 10 || adjusted.Delta != -2 {
					t.Errorf("payload = %+v, want the resulting stock and the delta", adjusted)
				}
			},
		},
		{
			// Variant events change nothing in this index, but they still have
			// to parse — see the package comment: decoding keeps a broken
			// producer a visible failure instead of a silent no-op.
			name:      "variant created",
			eventType: event.TypeVariantCreated,
			payload:   `{"id": 9, "product_id": 7, "sku": "KAOS-M", "name": "M", "stock": 4}`,
			check: func(t *testing.T, payload any) {
				snapshot, ok := payload.(*event.VariantSnapshot)
				if !ok {
					t.Fatalf("payload is %T, want *event.VariantSnapshot", payload)
				}
				if snapshot.ProductID != 7 || snapshot.SKU != "KAOS-M" {
					t.Errorf("snapshot = %+v", snapshot)
				}
			},
		},
		{
			name:      "variant updated",
			eventType: event.TypeVariantUpdated,
			payload:   `{"id": 9, "product_id": 7, "sku": "KAOS-M"}`,
			check: func(t *testing.T, payload any) {
				if _, ok := payload.(*event.VariantSnapshot); !ok {
					t.Fatalf("payload is %T, want *event.VariantSnapshot", payload)
				}
			},
		},
		{
			name:      "variant deleted",
			eventType: event.TypeVariantDeleted,
			payload:   `{"product_id": 7, "variant_id": 9}`,
			check: func(t *testing.T, payload any) {
				deleted, ok := payload.(*event.VariantDeletedPayload)
				if !ok {
					t.Fatalf("payload is %T, want *event.VariantDeletedPayload", payload)
				}
				if deleted.ProductID != 7 || deleted.VariantID != 9 {
					t.Errorf("payload = %+v", deleted)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, payload, err := event.Decode([]byte(envelope(tt.eventType, tt.payload)))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if env.EventType != tt.eventType {
				t.Errorf("envelope type = %q, want %q", env.EventType, tt.eventType)
			}
			if env.EventID == "" {
				t.Error("envelope has no event id")
			}
			tt.check(t, payload)
		})
	}
}

// TestDecodeSkipsUnknownEventTypes pins forward compatibility: a newer
// producer may emit a type this version does not know, and that is a skip
// with a nil payload rather than a failure — otherwise a routine producer
// upgrade would fill the dead-letter topic.
func TestDecodeSkipsUnknownEventTypes(t *testing.T) {
	t.Parallel()

	env, payload, err := event.Decode([]byte(envelope("product.v2.product.renamed", `{"id":1}`)))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env == nil || env.EventType != "product.v2.product.renamed" {
		t.Errorf("envelope = %+v", env)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil for an unknown type", payload)
	}
}

// TestDecodeFailuresArePermanent pins the classification the consumer's retry
// policy depends on: every decode failure is permanent by construction, since
// the message bytes are fixed and a retry produces the identical failure.
func TestDecodeFailuresArePermanent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "malformed JSON",
			body: `{"event_id": `,
			want: "decode event envelope",
		},
		{
			name: "missing event id",
			body: `{"event_type":"product.v1.product.created","schema_version":1,"payload":{}}`,
			want: "no event_id",
		},
		{
			name: "future schema version",
			body: `{"event_id":"e1","event_type":"product.v1.product.created","schema_version":2,"payload":{}}`,
			want: "schema version 2",
		},
		{
			name: "payload does not match its type",
			body: envelope(event.TypeProductCreated, `{"id":"not-a-number"}`),
			want: "decode product.v1.product.created payload",
		},
		{
			name: "typed event with no payload",
			body: `{"event_id":"e1","event_type":"product.v1.product.created","schema_version":1}`,
			want: "decode product.v1.product.created payload",
		},
		{
			// A variant payload that cannot parse must fail the same way: the
			// consumer dead-letters it instead of silently ignoring it.
			name: "malformed variant payload",
			body: envelope(event.TypeVariantCreated, `{"id":"broken"}`),
			want: "decode product.v1.variant.created payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := event.Decode([]byte(tt.body))
			if err == nil {
				t.Fatal("Decode accepted a body it should reject")
			}
			if !event.IsPermanent(err) {
				t.Errorf("error %q is not classified as permanent", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err, tt.want)
			}
		})
	}
}

// TestPermanentIsDetectableThroughWrapping pins that a permanent failure keeps
// its classification when the consumer adds context, and that an ordinary
// error is never mistaken for one.
func TestPermanentIsDetectableThroughWrapping(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	wrapped := errors.Join(errors.New("consumer: applying event"), event.Permanent(cause))

	if !event.IsPermanent(wrapped) {
		t.Error("IsPermanent lost the classification through wrapping")
	}
	if !errors.Is(wrapped, cause) {
		t.Error("the original cause is no longer reachable with errors.Is")
	}
	if event.IsPermanent(errors.New("transient: kafka unavailable")) {
		t.Error("IsPermanent classified an ordinary error as permanent")
	}
}
