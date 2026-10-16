// Package repository provides the domain event recorder implementation.
package repository

import (
	"product-service/internal/domain/product"
	"product-service/internal/event"
	"product-service/internal/ports"
)

// DomainEventRecorder implements ports.EventRecorder.
// It turns stored domain entities into domain events for the outbox.
//
// The payload written for each event type is the wire contract the read-side
// services decode (product-listing-service and product-search-service duplicate
// the same structs in their internal/event packages). Snapshot events carry the
// entity itself, whose MarshalJSON renders that snapshot; the small payloads
// are the shared types from internal/event.
type DomainEventRecorder struct{}

// NewDomainEventRecorder creates a new domain event recorder.
func NewDomainEventRecorder() *DomainEventRecorder {
	return &DomainEventRecorder{}
}

// Compile-time interface check
var _ ports.EventRecorder = (*DomainEventRecorder)(nil)

// ProductCreated returns events for a newly created product and its variants.
func (d *DomainEventRecorder) ProductCreated(p *product.Product, variants []*product.Variant) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventProductCreated,
		"product",
		p.ID,
		p.ID,
		p,
	)
	if err != nil {
		return nil, err
	}
	events := []*product.DomainEvent{event}

	for _, v := range variants {
		variantEvent, err := product.NewDomainEvent(
			product.EventVariantCreated,
			"product",
			v.ID,
			p.ID, // Key by parent product for ordering
			v,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, variantEvent)
	}
	return events, nil
}

// ProductUpdated returns events for an updated product.
func (d *DomainEventRecorder) ProductUpdated(p *product.Product) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventProductUpdated,
		"product",
		p.ID,
		p.ID,
		p,
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}

// ProductDeleted returns events for a deleted product.
func (d *DomainEventRecorder) ProductDeleted(productID int64) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventProductDeleted,
		"product",
		productID,
		productID,
		event.ProductDeletedPayload{ID: productID},
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}

// ProductStockAdjusted returns events for a stock adjustment.
func (d *DomainEventRecorder) ProductStockAdjusted(p *product.Product, delta int64) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventProductStockAdjusted,
		"product",
		p.ID,
		p.ID,
		event.ProductStockAdjustedPayload{ID: p.ID, Stock: p.Stock, Delta: delta},
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}

// VariantCreated returns events for a newly created variant.
func (d *DomainEventRecorder) VariantCreated(v *product.Variant) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventVariantCreated,
		"product",
		v.ID,
		v.ProductID, // Key by parent product
		v,
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}

// VariantUpdated returns events for an updated variant.
func (d *DomainEventRecorder) VariantUpdated(v *product.Variant) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventVariantUpdated,
		"product",
		v.ID,
		v.ProductID, // Key by parent product
		v,
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}

// VariantDeleted returns events for a deleted variant.
func (d *DomainEventRecorder) VariantDeleted(productID, variantID int64) ([]*product.DomainEvent, error) {
	event, err := product.NewDomainEvent(
		product.EventVariantDeleted,
		"product",
		variantID,
		productID, // Key by parent product
		event.VariantDeletedPayload{ProductID: productID, VariantID: variantID},
	)
	if err != nil {
		return nil, err
	}
	return []*product.DomainEvent{event}, nil
}
