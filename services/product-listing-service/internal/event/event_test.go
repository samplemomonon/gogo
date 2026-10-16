package event

import (
	"encoding/json"
	"strings"
	"testing"
)

// The consumer has to route on the envelope before it can understand the body, so
// the envelope's shape is the contract this test pins down.
func TestDecodeReadsTheEnvelope(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"event_id": "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"event_type": "product.v1.product.created",
		"schema_version": 1,
		"aggregate_type": "product",
		"aggregate_id": 42,
		"occurred_at": "2026-09-30T12:00:00Z",
		"payload": {"id": 42, "name": "Aurora Phone", "price_cents": 12999000}
	}`)

	envelope, payload, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if envelope.EventID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" {
		t.Errorf("event id = %q", envelope.EventID)
	}
	if envelope.AggregateID != 42 {
		t.Errorf("aggregate id = %d, want 42", envelope.AggregateID)
	}
	if envelope.OccurredAt.IsZero() {
		t.Error("occurred at is zero, want it parsed")
	}

	snapshot, ok := payload.(*ProductSnapshot)
	if !ok {
		t.Fatalf("payload = %T, want *ProductSnapshot", payload)
	}
	if snapshot.Name != "Aurora Phone" || snapshot.PriceCents != 12999000 {
		t.Errorf("payload = %+v, want the published product", snapshot)
	}
}

// Each event type must decode into its own struct. A mismatched pair here would
// silently write the wrong columns.
func TestDecodePicksThePayloadType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		eventType string
		payload   any
		want      any
	}{
		{
			name:      "product created",
			eventType: TypeProductCreated,
			payload:   ProductSnapshot{ID: 1, Name: "Thing"},
			want:      &ProductSnapshot{},
		},
		{
			name:      "product updated",
			eventType: TypeProductUpdated,
			payload:   ProductSnapshot{ID: 1, Name: "Thing"},
			want:      &ProductSnapshot{},
		},
		{
			name:      "product deleted",
			eventType: TypeProductDeleted,
			payload:   ProductDeletedPayload{ID: 1},
			want:      &ProductDeletedPayload{},
		},
		{
			name:      "stock adjusted",
			eventType: TypeProductStockAdjusted,
			payload:   ProductStockAdjustedPayload{ID: 1, Stock: 7, Delta: -3},
			want:      &ProductStockAdjustedPayload{},
		},
		{
			name:      "variant created",
			eventType: TypeVariantCreated,
			payload:   VariantSnapshot{ID: 5, ProductID: 1, SKU: "AUR-15"},
			want:      &VariantSnapshot{},
		},
		{
			name:      "variant updated",
			eventType: TypeVariantUpdated,
			payload:   VariantSnapshot{ID: 5, ProductID: 1, SKU: "AUR-15"},
			want:      &VariantSnapshot{},
		},
		{
			name:      "variant deleted",
			eventType: TypeVariantDeleted,
			payload:   VariantDeletedPayload{ProductID: 1, VariantID: 5},
			want:      &VariantDeletedPayload{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, payload, err := Decode(build(t, tc.eventType, tc.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if gotType, wantType := typeName(payload), typeName(tc.want); gotType != wantType {
				t.Errorf("payload type = %s, want %s", gotType, wantType)
			}
		})
	}
}

// An unrecognised type yields a nil payload and no error. The projector uses that
// to skip a message a newer producer emitted, rather than stalling the partition
// on a record that is perfectly valid.
func TestDecodeReturnsNilForAnUnknownType(t *testing.T) {
	t.Parallel()

	envelope, payload, err := Decode(build(t, "product.v9.product.teleported", map[string]any{"id": 1}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %T, want nil for an unknown type", payload)
	}
	// The envelope is still returned, so the id can be logged.
	if envelope.EventID == "" {
		t.Error("event id is empty, want it available for logging")
	}
}

func TestDecodeRejectsBadMessages(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":           `this is not json`,
		"missing event id":   `{"event_type":"product.v1.product.created","schema_version":1,"payload":{}}`,
		"future version":     `{"event_id":"x","event_type":"product.v1.product.created","schema_version":2,"payload":{}}`,
		"payload not object": `{"event_id":"x","event_type":"product.v1.product.created","schema_version":1,"payload":"text"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := Decode([]byte(body)); err == nil {
				t.Error("err = nil, want a decode failure")
			}
		})
	}
}

// A version mismatch has to be a hard error rather than a best-effort parse. The
// two services deploy independently, and silently reading a v2 envelope with v1
// rules would corrupt the read model in a way that is very hard to trace back.
func TestDecodeRejectsAFutureSchemaVersion(t *testing.T) {
	t.Parallel()

	_, _, err := Decode([]byte(
		`{"event_id":"x","event_type":"product.v1.product.created","schema_version":99,"payload":{}}`,
	))
	if err == nil {
		t.Fatal("err = nil, want a version mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error %q does not mention the version", err)
	}
}

// Every decode failure is permanent, and the distinction is what routes a record
// to the dead-letter topic rather than a retry loop. A record that can never
// decode would otherwise hold its partition and stop every product behind it from
// ever being read.
func TestDecodeFailuresAreClassifiedAsPermanent(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":           `this is not json`,
		"missing event id":   `{"event_type":"product.v1.product.created","schema_version":1,"payload":{}}`,
		"future version":     `{"event_id":"x","event_type":"product.v1.product.created","schema_version":2,"payload":{}}`,
		"payload not object": `{"event_id":"x","event_type":"product.v1.product.created","schema_version":1,"payload":"text"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := Decode([]byte(body))
			if err == nil {
				t.Fatal("err = nil, want a decode failure")
			}
			if !IsPermanent(err) {
				t.Errorf("IsPermanent(%v) = false, want true: this record can never decode", err)
			}
		})
	}
}

// A successfully decoded message is not permanent, so it is never dead-lettered.
func TestDecodedMessagesAreNotPermanent(t *testing.T) {
	t.Parallel()

	if _, _, err := Decode(build(t, TypeProductCreated, ProductSnapshot{ID: 1})); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// An unknown type decodes cleanly and is skipped rather than rejected, so it
	// must not be classified as permanent either: a newer producer's event is
	// perfectly valid, and this version simply has nothing to do with it.
	_, payload, err := Decode(build(t, "product.v9.thing", map[string]any{"id": 1}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if payload != nil {
		t.Error("payload should be nil for an unknown type")
	}
	if IsPermanent(nil) {
		t.Error("IsPermanent(nil) = true, want false")
	}
}

func build(t *testing.T, eventType string, payload any) []byte {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body, err := json.Marshal(Envelope{
		EventID:       "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		EventType:     eventType,
		SchemaVersion: SchemaVersion,
		AggregateType: "product",
		AggregateID:   1,
		Payload:       raw,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

func typeName(v any) string {
	switch v.(type) {
	case *ProductSnapshot:
		return "ProductSnapshot"
	case *ProductDeletedPayload:
		return "ProductDeletedPayload"
	case *ProductStockAdjustedPayload:
		return "ProductStockAdjustedPayload"
	case *VariantSnapshot:
		return "VariantSnapshot"
	case *VariantDeletedPayload:
		return "VariantDeletedPayload"
	default:
		return "unknown"
	}
}
