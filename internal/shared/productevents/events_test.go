package productevents

import (
	"context"
	"errors"
	"testing"

	"gogo/internal/shared/events"
)

// record builds a Kafka record value in the platform's envelope shape.
func record(eventType string, aggregateType string, aggregateID int64, payload string) []byte {
	return []byte(`{
		"event_id": "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"event_type": "` + eventType + `",
		"schema_version": 1,
		"aggregate_type": "` + aggregateType + `",
		"aggregate_id": ` + itoa(aggregateID) + `,
		"occurred_at": "2026-09-30T12:00:00Z",
		"payload": ` + payload + `
	}`)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// A product snapshot carries its id at the top level, and that id is what a consumer
// keys its cache on — so it must survive the decode exactly.
func TestDecodeProductSnapshotCarriesTheProductID(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		eventType string
		wantType  string
	}{
		{"product.v1.product.created", ProductCreated},
		{"product.v1.product.updated", ProductUpdated},
		{"product.v1.product.deleted", ProductDeleted},
		{"product.v1.product.stock_adjusted", ProductStockAdjusted},
	} {
		t.Run(tt.eventType, func(t *testing.T) {
			t.Parallel()

			event, err := Decode(record(tt.eventType, "product", 42,
				`{"id":42,"name":"Aurora Phone","price_cents":12999000,"stock":5}`))
			if err != nil {
				t.Fatalf("Decode = %v, want nil", err)
			}
			if event.Type != tt.wantType {
				t.Errorf("type = %q, want %q", event.Type, tt.wantType)
			}

			payload, ok := event.Payload.(Payload)
			if !ok {
				t.Fatalf("payload is %T, want Payload", event.Payload)
			}
			// The decimal string form is what the consumer clients hand back, so a
			// mismatch here would invalidate a key nothing else produces.
			if payload.ID != "42" {
				t.Errorf("ID = %q, want %q", payload.ID, "42")
			}
			if payload.AggregateType != "product" {
				t.Errorf("AggregateType = %q, want product", payload.AggregateType)
			}
		})
	}
}

// A variant event carries product_id, not id. Reading only payload.id would invalidate
// nothing at all here — a bug that shows up as a stale page, not as an error.
func TestDecodeVariantEventUsesTheProductIDNotTheVariantID(t *testing.T) {
	t.Parallel()

	event, err := Decode(record("product.v1.variant.updated", "variant", 900,
		`{"product_id":42,"variant_id":900,"sku":"AUR-128"}`))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}

	payload := event.Payload.(Payload)
	if payload.ID != "42" {
		t.Errorf("ID = %q, want the product id 42, not the variant id 900", payload.ID)
	}
	// A variant change invalidates the same caches a product change does.
	if event.Type != ProductUpdated {
		t.Errorf("type = %q, want %q", event.Type, ProductUpdated)
	}
	// The variant's own id is preserved for logging.
	if payload.AggregateID != 900 {
		t.Errorf("AggregateID = %d, want 900", payload.AggregateID)
	}
}

// A stock adjustment is not a metadata change: it carries the resulting quantity and
// the delta, and it must still map to a type a subscriber receives.
func TestDecodeStockAdjustmentCarriesTheProductID(t *testing.T) {
	t.Parallel()

	event, err := Decode(record("product.v1.product.stock_adjusted", "product", 7,
		`{"id":7,"stock":4,"delta":-3}`))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	if event.Type != ProductStockAdjusted {
		t.Errorf("type = %q, want %q", event.Type, ProductStockAdjusted)
	}
	if payload := event.Payload.(Payload); payload.ID != "7" {
		t.Errorf("ID = %q, want 7", payload.ID)
	}
}

// A tombstone is what a compacted topic emits on delete. It must be skipped, not
// treated as a decode failure that would block the partition.
func TestDecodeTombstoneIsSkippedNotFailed(t *testing.T) {
	t.Parallel()

	_, err := Decode(nil)
	if !errors.Is(err, ErrSkip) {
		t.Errorf("Decode(nil) = %v, want ErrSkip", err)
	}
}

// An unknown event type means a newer producer on the same topic. Skipping it is
// correct; guessing at its payload would be worse than ignoring it.
func TestDecodeUnknownEventTypeIsSkipped(t *testing.T) {
	t.Parallel()

	_, err := Decode(record("product.v2.product.repriced", "product", 1, `{"id":1}`))
	if !errors.Is(err, ErrSkip) {
		t.Errorf("Decode = %v, want ErrSkip", err)
	}
}

// A schema version this build does not understand must not be misread as v1: doing so
// would attribute one producer's payload shape to another's semantics.
func TestDecodeRejectsAnUnknownSchemaVersion(t *testing.T) {
	t.Parallel()

	value := []byte(`{"event_id":"x","event_type":"product.v1.product.created","schema_version":2,"aggregate_type":"product","aggregate_id":1,"payload":{"id":1}}`)
	_, err := Decode(value)
	if !errors.Is(err, ErrUndecodable) {
		t.Errorf("Decode = %v, want ErrUndecodable", err)
	}
}

// A payload with no product id anywhere cannot be attributed to anything, and
// republishing it would invalidate an unpredictable key. It is a decode failure.
func TestDecodeRejectsAPayloadWithNoProductID(t *testing.T) {
	t.Parallel()

	_, err := Decode(record("product.v1.product.updated", "product", 1, `{"name":"no id here"}`))
	if !errors.Is(err, ErrUndecodable) {
		t.Errorf("Decode = %v, want ErrUndecodable", err)
	}
}

func TestDecodeRejectsAnEmptyPayload(t *testing.T) {
	t.Parallel()

	_, err := Decode(record("product.v1.product.created", "product", 1, `{}`))
	if !errors.Is(err, ErrUndecodable) {
		t.Errorf("Decode = %v, want ErrUndecodable for a payload with neither id nor product_id", err)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := Decode([]byte("{not json"))
	if !errors.Is(err, ErrUndecodable) {
		t.Errorf("Decode = %v, want ErrUndecodable", err)
	}
}

// A tenant on the payload is carried through; its absence is reported as empty rather
// than guessed at, so a consumer can tell "the platform said nothing" from a value it
// invented.
func TestDecodeCarriesATenantWhenThePayloadHasOne(t *testing.T) {
	t.Parallel()

	event, err := Decode(record("product.v1.product.updated", "product", 1,
		`{"id":1,"tenant_id":"acme"}`))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	if payload := event.Payload.(Payload); payload.TenantID != "acme" {
		t.Errorf("TenantID = %q, want acme", payload.TenantID)
	}
}

func TestDecodeLeavesTenantEmptyWhenAbsent(t *testing.T) {
	t.Parallel()

	event, err := Decode(record("product.v1.product.updated", "product", 1, `{"id":1}`))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	if payload := event.Payload.(Payload); payload.TenantID != "" {
		t.Errorf("TenantID = %q, want empty rather than a guessed default", payload.TenantID)
	}
}

// Types must cover every type Decode can emit, or a module would subscribe to a
// vocabulary that silently omits an event kind.
func TestTypesCoversEveryEmittedType(t *testing.T) {
	t.Parallel()

	emitted := map[string]struct{}{}
	for wire := range eventTypes {
		event, err := Decode(record(wire, "product", 1, `{"id":1}`))
		if err != nil {
			t.Fatalf("Decode(%q) = %v", wire, err)
		}
		emitted[event.Type] = struct{}{}
	}

	for _, typ := range Types() {
		if _, ok := emitted[typ]; !ok {
			t.Errorf("Types() lists %q, which no wire event maps to", typ)
		}
		delete(emitted, typ)
	}
	for typ := range emitted {
		t.Errorf("wire event emits %q, which Types() omits", typ)
	}
}

// The publisher is the whole point of the package: a decoded record has to reach the
// in-process bus, and a skipped one has to reach nothing.
func TestPublisherRepublishesDecodedEventsOntoTheBus(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	var got []events.Event
	bus.Subscribe(func(_ context.Context, event events.Event) {
		got = append(got, event)
	})

	publisher := NewPublisher(bus)
	if err := publisher.Publish(context.Background(),
		record("product.v1.product.created", "product", 42, `{"id":42}`)); err != nil {
		t.Fatalf("Publish = %v, want nil", err)
	}

	if len(got) != 1 {
		t.Fatalf("bus received %d events, want 1", len(got))
	}
	if got[0].Type != ProductCreated {
		t.Errorf("type = %q, want %q", got[0].Type, ProductCreated)
	}
	if payload := got[0].Payload.(Payload); payload.ID != "42" {
		t.Errorf("ID = %q, want 42", payload.ID)
	}
}

func TestPublisherDoesNotPublishASkippedRecord(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	published := 0
	bus.Subscribe(func(context.Context, events.Event) { published++ })

	publisher := NewPublisher(bus)
	if err := publisher.Publish(context.Background(), nil); !errors.Is(err, ErrSkip) {
		t.Fatalf("Publish = %v, want ErrSkip", err)
	}
	if published != 0 {
		t.Errorf("bus received %d events for a tombstone, want 0", published)
	}
}

// A contract-keyed subscriber must receive exactly the product-changed vocabulary and
// nothing else — that is the guarantee a module relies on to avoid a type switch.
func TestASubscriberReceivesOnlyTheProductVocabulary(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	var seen []string
	sub := bus.SubscribeTypes(func(_ context.Context, event events.Event) {
		seen = append(seen, event.Type)
	}, Types()...)
	defer sub.Cancel()

	publisher := NewPublisher(bus)
	ctx := context.Background()
	for _, wire := range []string{
		"product.v1.product.created",
		"product.v1.variant.updated",
		"product.v1.product.stock_adjusted",
	} {
		if err := publisher.Publish(ctx, record(wire, "product", 1, `{"id":1,"product_id":1}`)); err != nil {
			t.Fatalf("Publish(%q) = %v", wire, err)
		}
	}

	// Something entirely unrelated on the same bus must not reach the subscriber.
	bus.Publish(ctx, events.Event{Type: "auth.session.opened", Payload: "not a product event"})

	if len(seen) != 3 {
		t.Fatalf("subscriber saw %v, want exactly the three product events", seen)
	}
	for _, typ := range seen {
		found := false
		for _, want := range Types() {
			if typ == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("subscriber received %q, which is outside Types()", typ)
		}
	}
}

func TestNewConsumerRequiresBrokers(t *testing.T) {
	t.Parallel()

	if _, err := NewConsumer(nil, ConsumerGroup, func(context.Context, []byte) error { return nil }, nil); err == nil {
		t.Error("NewConsumer with no brokers = nil error, want a failure")
	}
}

func TestNewConsumerRequiresAHandler(t *testing.T) {
	t.Parallel()

	if _, err := NewConsumer([]string{"localhost:9092"}, ConsumerGroup, nil, nil); err == nil {
		t.Error("NewConsumer with a nil handler = nil error, want a failure")
	}
}

func TestConsumerGroupIsDistinctFromTheProjections(t *testing.T) {
	t.Parallel()

	// Kafka splits partitions *within* a group. Sharing one with a projection consumer
	// would hand this gateway a fraction of the stream and every event it dropped
	// would be a cache entry it never invalidated.
	if ConsumerGroup == "product-listing-service" || ConsumerGroup == "product-search-service" {
		t.Errorf("ConsumerGroup = %q, which collides with a projection consumer", ConsumerGroup)
	}
}
