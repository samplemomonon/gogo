// Package inventoryclient adapts the inventory.v1 contract to the sales module's
// StockAdjuster port, so the sales module can apply a sale's stock effect without
// importing inventory internals.
package inventoryclient

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/features/sales/application/ports"
	"gogo/internal/features/sales/domain"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/tenant"
)

// Client implements ports.StockAdjuster over the inventory.v1 gRPC contract.
type Client struct {
	inventory  inventoryv1.InventoryServiceClient
	breaker    *breaker.Breaker
	capability authn.Capability
}

var _ ports.StockAdjuster = (*Client)(nil)

func New(conn grpc.ClientConnInterface, b *breaker.Breaker, capabilities ...authn.Capability) *Client {
	var capability authn.Capability
	if len(capabilities) > 0 {
		capability = capabilities[0]
	}
	return &Client{inventory: inventoryv1.NewInventoryServiceClient(conn), breaker: b, capability: capability}
}

// Adjust applies a signed stock delta through the inventory service. The tenant
// rides call metadata, so inventory scopes the adjustment to the right website.
func (c *Client) Adjust(ctx context.Context, tenantID domain.Tenant, productID string, delta int, kind, reference string) error {
	ctx = tenant.WithID(ctx, tenant.ID(tenantID))
	ctx = authn.WithService(ctx, authn.SalesCaller, c.capability)
	var rpcErr error
	err := c.breaker.Execute(func() error {
		_, e := c.inventory.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
			ProductId: productID,
			Delta:     int32(delta),
			Kind:      kind,
			Reference: reference,
		})
		rpcErr = e
		return classify(e)
	})
	if err != nil {
		return err
	}
	return mapError(rpcErr)
}

// classify reports a business rejection as success to the breaker, so an expected
// refusal such as insufficient stock does not open the circuit.
func classify(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument:
		return nil
	default:
		return err
	}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.FailedPrecondition:
		return domain.ErrInsufficientStock
	case codes.NotFound:
		return domain.ErrProductNotFound
	default:
		return err
	}
}
