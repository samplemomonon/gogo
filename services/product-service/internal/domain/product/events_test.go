package product

import (
	"encoding/json"
	"testing"
	"time"
)

// The payload shapes are pinned byte-for-byte because they are the producer
// half of a cross-service contract: product-listing-service and
// product-search-service decode these bytes from the Kafka topic with their own
// structs, so a rename here silently zeroes a field there.

func TestProductMarshalJSONIsWireSnapshot(t *testing.T) {
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 2, 11, 30, 0, 0, time.UTC)
	description := "6.7 inch OLED"
	brand := "Aurora"
	mediaID := int64(9)

	p := &Product{
		ID:             7,
		CategoryID:     3,
		Name:           "Aurora Phone 15 Pro",
		Slug:           "aurora-phone-15-pro",
		Description:    &description,
		Brand:          &brand,
		Price:          Money{Amount: 12_999_000, Currency: "IDR"},
		Stock:          25,
		Status:         StatusActive,
		PrimaryMediaID: &mediaID,
		CreatedAt:      created,
		UpdatedAt:      updated,
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"id":7,"category_id":3,"name":"Aurora Phone 15 Pro","slug":"aurora-phone-15-pro",` +
		`"description":"6.7 inch OLED","brand":"Aurora","price_cents":12999000,"currency":"IDR",` +
		`"stock":25,"status":"active","primary_media_id":9,` +
		`"created_at":"2026-03-01T10:00:00Z","updated_at":"2026-03-02T11:30:00Z"}`
	if string(data) != want {
		t.Fatalf("product snapshot:\n got %s\nwant %s", data, want)
	}
}

func TestProductMarshalJSONNullableFields(t *testing.T) {
	p := &Product{ID: 1, Price: Money{Currency: "IDR"}}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"description", "brand", "primary_media_id"} {
		if raw, ok := got[key]; !ok || string(raw) != "null" {
			t.Fatalf("%s = %s (present %v), want null", key, raw, ok)
		}
	}
	if _, ok := got["variants"]; ok {
		t.Fatal("variants must be omitted when empty")
	}
	if _, ok := got["Price"]; ok {
		t.Fatal("the nested PascalCase Price object must not be on the wire")
	}
}

func TestVariantMarshalJSONIsWireSnapshot(t *testing.T) {
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 2, 11, 30, 0, 0, time.UTC)
	mediaID := int64(4)

	v := &Variant{
		ID:        11,
		ProductID: 7,
		SKU:       "AURORA-15-PRO-256-BLK",
		Name:      "256GB Black",
		Price:     Money{Amount: 12_999_000, Currency: "IDR"},
		Stock:     10,
		MediaID:   &mediaID,
		CreatedAt: created,
		UpdatedAt: updated,
	}

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"id":11,"product_id":7,"sku":"AURORA-15-PRO-256-BLK","name":"256GB Black",` +
		`"price_cents":12999000,"currency":"IDR","stock":10,"media_id":4,` +
		`"created_at":"2026-03-01T10:00:00Z","updated_at":"2026-03-02T11:30:00Z"}`
	if string(data) != want {
		t.Fatalf("variant snapshot:\n got %s\nwant %s", data, want)
	}
}

func TestNewDomainEvent(t *testing.T) {
	evt, err := NewDomainEvent(EventProductCreated, "product", 1, 1, &Product{ID: 1, Name: "Test"})
	if err != nil {
		t.Fatalf("NewDomainEvent: %v", err)
	}
	if evt.EventType != EventProductCreated {
		t.Errorf("EventType = %v, want %v", evt.EventType, EventProductCreated)
	}
	if evt.AggregateID != 1 {
		t.Errorf("AggregateID = %d, want 1", evt.AggregateID)
	}
	if evt.Topic != "product.events.v1" {
		t.Errorf("Topic = %q, want %q", evt.Topic, "product.events.v1")
	}
	if evt.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", evt.SchemaVersion)
	}
}

func TestDomainEventMarshal(t *testing.T) {
	evt, err := NewDomainEvent(EventProductUpdated, "product", 42, 42, &Product{ID: 42, Name: "X", Price: Money{Currency: "IDR"}})
	if err != nil {
		t.Fatalf("NewDomainEvent: %v", err)
	}
	evt.EventID = "evt-1"
	evt.OccurredAt = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	data, err := evt.Marshal()
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
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.EventID != "evt-1" || got.EventType != string(EventProductUpdated) || got.SchemaVersion != 1 {
		t.Fatalf("envelope headers = %+v", got)
	}
	if got.AggregateType != "product" || got.AggregateID != 42 {
		t.Fatalf("aggregate = (%q, %d), want (product, 42)", got.AggregateType, got.AggregateID)
	}
	if !got.OccurredAt.Equal(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("occurred_at = %v", got.OccurredAt)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("payload is not an object: %v", err)
	}
	if _, ok := payload["name"]; !ok {
		t.Fatalf("payload must be the flat snapshot, got %s", got.Payload)
	}
}

func TestNewVariantEventPartitionKey(t *testing.T) {
	evt, err := NewDomainEvent(EventVariantCreated, "product", 100, 5, &Variant{ID: 100, ProductID: 5})
	if err != nil {
		t.Fatalf("NewDomainEvent: %v", err)
	}
	if evt.PartitionKey != "5" {
		t.Errorf("PartitionKey = %q, want %q", evt.PartitionKey, "5")
	}
}

func TestEventTypesAreValid(t *testing.T) {
	events := []DomainEventType{
		EventProductCreated, EventProductUpdated, EventProductDeleted, EventProductStockAdjusted,
		EventVariantCreated, EventVariantUpdated, EventVariantDeleted,
	}
	seen := make(map[DomainEventType]bool)
	for _, e := range events {
		if seen[e] {
			t.Errorf("duplicate event type: %s", e)
		}
		seen[e] = true
		if e == "" {
			t.Error("empty event type")
		}
	}
}
