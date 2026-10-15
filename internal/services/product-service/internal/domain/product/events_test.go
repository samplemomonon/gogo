package product

import (
	"encoding/json"
	"testing"
)

func TestNewDomainEvent(t *testing.T) {
	evt, err := NewDomainEvent(EventProductCreated, "product", 1, 1, ProductCreatedEvent{Product: &Product{Name: "Test"}})
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
	evt, _ := NewDomainEvent(EventProductUpdated, "product", 42, 42, ProductUpdatedEvent{Product: &Product{Name: "X"}})
	data, err := evt.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["aggregate_type"] != "product" {
		t.Errorf("aggregate_type = %v, want product", got["aggregate_type"])
	}
	if got["schema_version"].(float64) != 1 {
		t.Errorf("schema_version = %v, want 1", got["schema_version"])
	}
}

func TestNewVariantEventPartitionKey(t *testing.T) {
	evt, err := NewDomainEvent(EventVariantCreated, "product", 100, 5, VariantCreatedEvent{})
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
