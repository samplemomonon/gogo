//go:build wireinject

package product

import (
	"github.com/google/wire"
	"product-service/internal/ports"
)

// UseCaseSet provides the product use cases.
var UseCaseSet = wire.NewSet(
	NewCreateProductUseCase,
	NewUpdateProductUseCase,
	NewDeleteProductUseCase,
	NewAdjustStockUseCase,
	NewCreateVariantUseCase,
	NewUpdateVariantUseCase,
	NewDeleteVariantUseCase,
	NewProductUseCaseFacade,
	wire.Bind(new(ProductUseCases), new(*ProductUseCaseFacade)),
)
