//go:build wireinject

package http

import (
	"github.com/google/wire"
	"product-service/internal/usecase/product"
)

// HandlerSet provides the HTTP handlers.
var HandlerSet = wire.NewSet(
	NewProductHandler,
	NewHealthHandler,
)
