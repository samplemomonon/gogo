//go:build wireinject

package repository

import (
	"github.com/google/wire"
	"product-service/internal/ports"
)

// RepositorySet provides the repository implementations.
var RepositorySet = wire.NewSet(
	NewProductPostgres,
	NewDomainEventRecorder,
	wire.Bind(new(ports.ProductRepository), new(*ProductPostgres)),
	wire.Bind(new(ports.VariantRepository), new(*ProductPostgres)),
	wire.Bind(new(ports.EventSink), new(*ProductPostgres)),
	wire.Bind(new(ports.EventRecorder), new(*DomainEventRecorder)),
)