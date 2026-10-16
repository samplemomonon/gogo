// Package product contains the use cases for product operations.
package product

import (
	"context"

	"product-service/internal/domain/product"
)

// ProductUseCaseFacade combines all product use cases into a single interface.
type ProductUseCaseFacade struct {
	createProduct *CreateProductUseCase
	updateProduct *UpdateProductUseCase
	deleteProduct *DeleteProductUseCase
	adjustStock   *AdjustStockUseCase
	createVariant *CreateVariantUseCase
	updateVariant *UpdateVariantUseCase
	deleteVariant *DeleteVariantUseCase
}

// NewProductUseCaseFacade creates a new facade with all use cases.
func NewProductUseCaseFacade(deps UseCaseDependencies) *ProductUseCaseFacade {
	return &ProductUseCaseFacade{
		createProduct: NewCreateProductUseCase(deps),
		updateProduct: NewUpdateProductUseCase(deps),
		deleteProduct: NewDeleteProductUseCase(deps),
		adjustStock:   NewAdjustStockUseCase(deps),
		createVariant: NewCreateVariantUseCase(deps),
		updateVariant: NewUpdateVariantUseCase(deps),
		deleteVariant: NewDeleteVariantUseCase(deps),
	}
}

// CreateProduct creates a new product.
func (f *ProductUseCaseFacade) CreateProduct(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error) {
	return f.createProduct.Execute(ctx, req)
}

// UpdateProduct updates an existing product.
func (f *ProductUseCaseFacade) UpdateProduct(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error) {
	return f.updateProduct.Execute(ctx, id, req)
}

// DeleteProduct deletes a product.
func (f *ProductUseCaseFacade) DeleteProduct(ctx context.Context, id int64) error {
	return f.deleteProduct.Execute(ctx, id)
}

// AdjustStock adjusts product stock.
func (f *ProductUseCaseFacade) AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error) {
	return f.adjustStock.Execute(ctx, id, delta)
}

// CreateVariant creates a new variant.
func (f *ProductUseCaseFacade) CreateVariant(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error) {
	return f.createVariant.Execute(ctx, productID, req)
}

// UpdateVariant updates an existing variant.
func (f *ProductUseCaseFacade) UpdateVariant(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error) {
	return f.updateVariant.Execute(ctx, productID, variantID, req)
}

// DeleteVariant deletes a variant.
func (f *ProductUseCaseFacade) DeleteVariant(ctx context.Context, productID, variantID int64) error {
	return f.deleteVariant.Execute(ctx, productID, variantID)
}

// Compile-time interface checks
var _ interface {
	CreateProduct(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error)
	UpdateProduct(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error)
	DeleteProduct(ctx context.Context, id int64) error
	AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error)
	CreateVariant(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error)
	UpdateVariant(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error)
	DeleteVariant(ctx context.Context, productID, variantID int64) error
} = (*ProductUseCaseFacade)(nil)
