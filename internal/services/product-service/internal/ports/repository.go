// Package ports defines the interfaces (ports) that the use cases depend on.
// These are implemented by infrastructure adapters.
package ports

import (
	"context"

	"product-service/internal/domain/product"
)

// ProductReader defines read operations for products.
type ProductReader interface {
	// GetByID returns a product by ID without variants.
	GetByID(ctx context.Context, id int64) (*product.Product, error)
	// GetByIDWithVariants returns a product by ID with its variants.
	GetByIDWithVariants(ctx context.Context, id int64) (*product.Product, error)
	// ExistsByID checks if a product exists.
	ExistsByID(ctx context.Context, id int64) (bool, error)
}

// ProductWriter defines write operations for products.
type ProductWriter interface {
	// CreateProduct inserts a new product with its variants.
	CreateProduct(ctx context.Context, product *product.Product, variants []*product.Variant) (*product.Product, error)
	// UpdateProduct modifies an existing product.
	UpdateProduct(ctx context.Context, product *product.Product) (*product.Product, error)
	// DeleteProduct removes a product and its variants.
	DeleteProduct(ctx context.Context, id int64) error
	// AdjustStock atomically adjusts product stock.
	AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error)
}

// VariantReader defines read operations for variants.
type VariantReader interface {
	// GetByID returns a variant by ID.
	GetVariantByID(ctx context.Context, id int64) (*product.Variant, error)
	// CountByProductID returns the number of variants for a product.
	CountByProductID(ctx context.Context, productID int64) (int, error)
}

// VariantWriter defines write operations for variants.
type VariantWriter interface {
	// CreateVariant inserts a new variant for a product.
	CreateVariant(ctx context.Context, variant *product.Variant) (*product.Variant, error)
	// UpdateVariant modifies an existing variant.
	UpdateVariant(ctx context.Context, variant *product.Variant) (*product.Variant, error)
	// DeleteVariant removes a variant.
	DeleteVariant(ctx context.Context, productID, variantID int64) error
}

// ProductRepository combines read and write operations for products.
// Use this when you need both read and write access.
type ProductRepository interface {
	ProductReader
	ProductWriter
}

// VariantRepository combines read and write operations for variants.
type VariantRepository interface {
	VariantReader
	VariantWriter
}

// CategoryLookup defines the interface for validating category references.
type CategoryLookup interface {
	// Exists checks which category IDs exist.
	Exists(ctx context.Context, ids []int64) (map[int64]bool, error)
}

// MediaLookup defines the interface for validating media references.
type MediaLookup interface {
	// Exists checks which media IDs exist.
	Exists(ctx context.Context, ids []int64) (map[int64]bool, error)
}

// EventRecorder defines the interface for recording domain events within a transaction.
// This is called by the repository after writing the aggregate, inside the same transaction.
type EventRecorder interface {
	// ProductCreated returns events for a newly created product and its variants.
	ProductCreated(product *product.Product, variants []*product.Variant) ([]*product.DomainEvent, error)
	// ProductUpdated returns events for an updated product.
	ProductUpdated(product *product.Product) ([]*product.DomainEvent, error)
	// ProductDeleted returns events for a deleted product.
	ProductDeleted(productID int64) ([]*product.DomainEvent, error)
	// ProductStockAdjusted returns events for a stock adjustment.
	ProductStockAdjusted(product *product.Product, delta int64) ([]*product.DomainEvent, error)
	// VariantCreated returns events for a newly created variant.
	VariantCreated(variant *product.Variant) ([]*product.DomainEvent, error)
	// VariantUpdated returns events for an updated variant.
	VariantUpdated(variant *product.Variant) ([]*product.DomainEvent, error)
	// VariantDeleted returns events for a deleted variant.
	VariantDeleted(productID, variantID int64) ([]*product.DomainEvent, error)
}

// EventSink defines the interface for writing events to the outbox within a transaction.
type EventSink interface {
	// Append writes events to the outbox inside the given transaction.
	Append(ctx context.Context, tx Transaction, events []*product.DomainEvent) ([]*product.DomainEvent, error)
}

// Transaction represents a database transaction.
// This is an abstraction to avoid importing database/sql in the domain layer.
type Transaction interface {
	// Commit commits the transaction.
	Commit() error
	// Rollback rolls back the transaction.
	Rollback() error
}

// KafkaEventPublisher defines the interface for publishing events to Kafka.
type KafkaEventPublisher interface {
	// Append publishes an event to Kafka.
	Append(ctx context.Context, event *product.DomainEvent) error
}

// OutboxEventRecorder defines the interface for recording derived events to the outbox.
// These are events that cannot be part of the original transaction (e.g., republishing a product after variant change).
type OutboxEventRecorder interface {
	// Append writes an event to the outbox in its own transaction.
	Append(ctx context.Context, event *product.DomainEvent) error
}

// OutboxRepository defines the interface for the outbox relay.
type OutboxRepository interface {
	// Claim reserves events for publishing.
	Claim(ctx context.Context, batchSize int, leaseDuration int64) ([]*product.DomainEvent, error)
	// MarkPublished marks events as published.
	MarkPublished(ctx context.Context, events []*product.DomainEvent) error
	// Reschedule schedules an event for retry.
	Reschedule(ctx context.Context, event *product.DomainEvent, cause error, retryAt int64) error
	// Fail marks an event as permanently failed.
	Fail(ctx context.Context, event *product.DomainEvent, cause error) error
	// Stats returns outbox statistics.
	Stats(ctx context.Context) (OutboxStats, error)
}

// OutboxStats contains statistics about the outbox.
type OutboxStats struct {
	Pending       int64
	Failed        int64
	OldestPending *int64 // Unix timestamp in milliseconds
}
