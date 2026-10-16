// Package event_test covers the wire contract product-service publishes: the
// record builders, the envelope, and the small payload bodies.
//
// The envelope shape is shared with the read-side consumers, which decode it
// with their own duplicated structs, so the key names and the schema version
// are pinned here rather than left to the compiler.
package event_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"product-service/internal/event"
)

func TestNewBuildsProductRecord(t *testing.T) {
	record, err := event.New(event.TypeProductUpdated, 42, event.ProductDeletedPayload{ID: 42})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if record.Topic != event.Topic {
		t.Fatalf("Topic = %q, want %q", record.Topic, event.Topic)
	}
	if record.EventType != event.TypeProductUpdated {
		t.Fatalf("EventType = %q", record.EventType)
	}
	if record.AggregateType != event.AggregateType {
		t.Fatalf("AggregateType = %q, want %q", record.AggregateType, event.AggregateType)
	}
	if record.AggregateID != 42 {
		t.Fatalf("AggregateID = %d, want 42", record.AggregateID)
	}
	if record.Key != "42" {
		t.Fatalf("Key = %q, want %q", record.Key, "42")
	}
	if string(record.Payload) != `{"id":42}` {
		t.Fatalf("Payload = %s, want {\"id\":42}", record.Payload)
	}
	if record.EventID != "" {
		t.Fatalf("EventID = %q, want empty: the database assigns it when the row is inserted", record.EventID)
	}
}

// TestNewVariantEventKeysByProduct pins the ordering guarantee: variant events
// carry the variant as their aggregate but partition by the parent product, so
// every event about one product stays on one partition.
func TestNewVariantEventKeysByProduct(t *testing.T) {
	record, err := event.NewVariantEvent(event.TypeVariantDeleted, 11, 7, event.VariantDeletedPayload{ProductID: 7, VariantID: 11})
	if err != nil {
		t.Fatalf("NewVariantEvent: %v", err)
	}

	if record.AggregateID != 11 {
		t.Fatalf("AggregateID = %d, want the variant id 11", record.AggregateID)
	}
	if record.Key != "7" {
		t.Fatalf("Key = %q, want the parent product id 7", record.Key)
	}
}

func TestNewRejectsUnmarshalablePayload(t *testing.T) {
	_, err := event.New(event.TypeProductUpdated, 1, func() {})
	if err == nil {
		t.Fatal("expected an error for a payload json.Marshal cannot encode")
	}
	if !strings.Contains(err.Error(), "encode "+event.TypeProductUpdated+" payload") {
		t.Fatalf("error = %q, want it to name the event type", err)
	}
}

func TestRecordMarshalEnvelope(t *testing.T) {
	occurred := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	record := event.Record{
		EventID:       "evt-1",
		Topic:         event.Topic,
		EventType:     event.TypeProductStockAdjusted,
		AggregateType: event.AggregateType,
		AggregateID:   7,
		Key:           "7",
		Payload:       []byte(`{"id":7,"stock":12,"delta":-3}`),
		OccurredAt:    occurred,
	}

	body, err := record.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		EventID       string          `json:"event_id"`
		EventType     string          `json:"event_type"`
		SchemaVersion int             `json:"schema_version"`
		AggregateType string          `json:"aggregate_type"`
		AggregateID   int64           `json:"aggregate_id"`
		OccurredAt    time.Time       `json:"occurred_at"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.EventID != "evt-1" || got.EventType != event.TypeProductStockAdjusted {
		t.Fatalf("headers = %+v", got)
	}
	if got.SchemaVersion != event.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, event.SchemaVersion)
	}
	if got.AggregateType != "product" || got.AggregateID != 7 {
		t.Fatalf("aggregate = (%q, %d)", got.AggregateType, got.AggregateID)
	}
	if !got.OccurredAt.Equal(occurred) {
		t.Fatalf("occurred_at = %v, want %v", got.OccurredAt, occurred)
	}
	if string(got.Payload) != `{"id":7,"stock":12,"delta":-3}` {
		t.Fatalf("payload = %s", got.Payload)
	}

	// The envelope the relay publishes must not leak Record bookkeeping.
	for _, key := range []string{"Attempts", "LeaseUntil", "Key", "Topic"} {
		if strings.Contains(string(body), `"`+key+`"`) {
			t.Fatalf("envelope must not contain %q: %s", key, body)
		}
	}
}

// TestPayloadBodiesAreTheConsumerContract pins the payload key names the
// read-side services decode.
func TestPayloadBodiesAreTheConsumerContract(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{
			name:    "product deleted",
			payload: event.ProductDeletedPayload{ID: 7},
			want:    `{"id":7}`,
		},
		{
			name:    "product stock adjusted",
			payload: event.ProductStockAdjustedPayload{ID: 7, Stock: 12, Delta: -3},
			want:    `{"id":7,"stock":12,"delta":-3}`,
		},
		{
			name:    "variant deleted",
			payload: event.VariantDeletedPayload{ProductID: 7, VariantID: 11},
			want:    `{"product_id":7,"variant_id":11}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(body) != tc.want {
				t.Fatalf("payload = %s, want %s", body, tc.want)
			}
		})
	}
}

func TestTopicsAndTypes(t *testing.T) {
	if event.Topic != "product.events.v1" {
		t.Fatalf("Topic = %q", event.Topic)
	}
	if event.AggregateType != "product" {
		t.Fatalf("AggregateType = %q", event.AggregateType)
	}
	if event.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d", event.SchemaVersion)
	}

	types := []string{
		event.TypeProductCreated, event.TypeProductUpdated, event.TypeProductDeleted,
		event.TypeProductStockAdjusted, event.TypeVariantCreated, event.TypeVariantUpdated,
		event.TypeVariantDeleted,
	}
	seen := make(map[string]bool)
	for _, tp := range types {
		if tp == "" {
			t.Fatal("empty event type")
		}
		if seen[tp] {
			t.Fatalf("duplicate event type %q", tp)
		}
		seen[tp] = true
		if !strings.HasPrefix(tp, "product.v1.") {
			t.Fatalf("event type %q must be fully qualified with the schema version", tp)
		}
	}
}
