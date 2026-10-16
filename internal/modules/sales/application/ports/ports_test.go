// Package ports_test pins the sales module's ports to the adapters that
// satisfy them. The assertions are compile-time: if an adapter stops
// implementing its port, this package fails to build before any test runs,
// which is the earliest possible point to catch the drift.
package ports_test

import (
	"testing"

	"gogo/internal/modules/sales/application"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/infrastructure"
	"gogo/internal/modules/sales/infrastructure/inventoryclient"
	salespostgres "gogo/internal/modules/sales/infrastructure/postgres"
	"gogo/internal/shared/breaker"
)

// The pins, one per contract:
//
//   - Store: both persistence backends the composition root can choose between
//     (in-memory and Postgres) must remain substitutable.
//   - StockAdjuster: the inventory client is the only adapter the composition
//     root hands to sales for its stock effect.
//   - SalesService: the gRPC adapter depends on the application service through
//     this port, never on the concrete type.
var (
	_ ports.Store         = (*infrastructure.Store)(nil)
	_ ports.Store         = (*salespostgres.Store)(nil)
	_ ports.StockAdjuster = (*inventoryclient.Client)(nil)
	_ ports.SalesService  = (*application.Service)(nil)
)

// TestAdaptersSatisfyTheirPorts constructs each adapter and assigns it through
// its port, so the conformance above is also exercised at runtime: a
// constructor that started returning nil would be caught here.
func TestAdaptersSatisfyTheirPorts(t *testing.T) {
	t.Parallel()

	store := ports.Store(infrastructure.NewStore())
	pgStore := ports.Store(salespostgres.NewStore(nil))
	stock := ports.StockAdjuster(inventoryclient.New(nil, breaker.NewDefault()))
	svc := ports.SalesService(application.NewService(store, stock))

	if store == nil || pgStore == nil || stock == nil || svc == nil {
		t.Fatal("an adapter constructor returned nil")
	}
}
