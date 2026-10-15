//go:build wireinject

package grpcserver

import (
	"github.com/google/wire"
	"product-service/internal/usecase/product"
)

// GRPCServerSet provides the gRPC server.
var GRPCServerSet = wire.NewSet(
	NewProductServer,
)