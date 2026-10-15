// Package product contains the use cases for product operations.
package product

import (
	"context"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// ProductUseCases defines the interface for all product and variant use cases.
type ProductUseCases interface {
	CreateProduct(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error)
	UpdateProduct(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error)
	DeleteProduct(ctx context.Context, id int64) error
	AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error)
	CreateVariant(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error)
	UpdateVariant(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error)
	DeleteVariant(ctx context.Context, productID, variantID int64) error
}

// UseCaseDependencies holds all dependencies needed by the use cases.
type UseCaseDependencies struct {
	ProductRepo     ports.ProductRepository
	VariantRepo     ports.VariantRepository
	CategoryLookup  ports.CategoryLookup
	MediaLookup     ports.MediaLookup
	EventRecorder   ports.EventRecorder
	EventSink       ports.EventSink
	KafkaPublisher  ports.KafkaEventPublisher
	OutboxRecorder  ports.OutboxEventRecorder
	DefaultCurrency string
}
