// Package product contains the core domain models and business rules for products.
package product

import (
	"encoding/json"
	"strconv"
	"time"
)

// DomainEventType identifies the type of a domain event.
type DomainEventType string

const (
	EventProductCreated       DomainEventType = "product.v1.product.created"
	EventProductUpdated       DomainEventType = "product.v1.product.updated"
	EventProductDeleted       DomainEventType = "product.v1.product.deleted"
	EventProductStockAdjusted DomainEventType = "product.v1.product.stock_adjusted"
	EventVariantCreated       DomainEventType = "product.v1.variant.created"
	EventVariantUpdated       DomainEventType = "product.v1.variant.updated"
	EventVariantDeleted       DomainEventType = "product.v1.variant.deleted"
)

// DomainEvent represents a domain event that occurred in the product aggregate.
type DomainEvent struct {
	EventID       string
	EventType     DomainEventType
	AggregateType string
	AggregateID   int64
	PartitionKey  string
	Payload       []byte
	OccurredAt    time.Time
	SchemaVersion int
	Topic         string
}

// NewDomainEvent creates a new domain event with the given payload.
func NewDomainEvent(eventType DomainEventType, aggregateType string, aggregateID, partitionKey int64, payload any) (*DomainEvent, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &DomainEvent{
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		PartitionKey:  strconv.FormatInt(partitionKey, 10),
		Payload:       body,
		SchemaVersion: 1,
		Topic:         "product.events.v1",
	}, nil
}

// Marshal returns the event as JSON for Kafka.
func (e *DomainEvent) Marshal() ([]byte, error) {
	type envelope struct {
		EventID       string          `json:"event_id"`
		EventType     string          `json:"event_type"`
		SchemaVersion int             `json:"schema_version"`
		AggregateType string          `json:"aggregate_type"`
		AggregateID   int64           `json:"aggregate_id"`
		OccurredAt    time.Time       `json:"occurred_at"`
		Payload       json.RawMessage `json:"payload"`
	}
	return json.Marshal(envelope{
		EventID:       e.EventID,
		EventType:     string(e.EventType),
		SchemaVersion: e.SchemaVersion,
		AggregateType: e.AggregateType,
		AggregateID:   e.AggregateID,
		OccurredAt:    e.OccurredAt,
		Payload:       e.Payload,
	})
}

// ProductCreatedEvent is emitted when a product is created.
type ProductCreatedEvent struct {
	Product  *Product
	Variants []*Variant
}

// ProductUpdatedEvent is emitted when a product is updated.
type ProductUpdatedEvent struct {
	Product *Product
}

// ProductDeletedEvent is emitted when a product is deleted.
type ProductDeletedEvent struct {
	ProductID int64
}

// ProductStockAdjustedEvent is emitted when product stock is adjusted.
type ProductStockAdjustedEvent struct {
	ProductID int64
	Stock     int64
	Delta     int64
}

// VariantCreatedEvent is emitted when a variant is created.
type VariantCreatedEvent struct {
	Variant   *Variant
	ProductID int64
}

// VariantUpdatedEvent is emitted when a variant is updated.
type VariantUpdatedEvent struct {
	Variant   *Variant
	ProductID int64
}

// VariantDeletedEvent is emitted when a variant is deleted.
type VariantDeletedEvent struct {
	ProductID int64
	VariantID int64
}
