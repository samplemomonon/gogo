// Package inventoryclient is the seller console's adapter over the inventory.v1 gRPC
// contract.
package inventoryclient

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/pages/seller/application/results"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// The inventory vocabulary in the caller's language. Every answer the contract
// can give is named, so the console can tell "the cashier asked to sell stock we
// do not have" from "the stock service is down" without inspecting a status code.
var (
	// ErrUnknownProduct is returned when the adjustment names a product the
	// inventory does not track.
	ErrUnknownProduct = errors.New("unknown product")
	// ErrInvalidAdjustment is returned when the adjustment itself is rejected,
	// such as a zero delta.
	ErrInvalidAdjustment = errors.New("invalid stock adjustment")
	// ErrInsufficientStock is returned when the remote inventory refuses an
	// adjustment that would drive stock below zero.
	ErrInsufficientStock = errors.New("insufficient stock")
	// ErrInventoryUnavailable is returned when the inventory service cannot be
	// reached.
	ErrInventoryUnavailable = errors.New("inventory unavailable")
)

var vocabulary = grpcconsumer.Vocabulary{
	NotFound:     ErrUnknownProduct,
	Invalid:      ErrInvalidAdjustment,
	Precondition: ErrInsufficientStock,
	Unavailable:  ErrInventoryUnavailable,
}

// Client adapts the generated inventory.v1 client to the seller page's ports.
type Client struct {
	inventory inventoryv1.InventoryServiceClient
	caller    *grpcconsumer.Caller
}

func New(conn grpc.ClientConnInterface, b *breaker.Breaker) *Client {
	return &Client{
		inventory: inventoryv1.NewInventoryServiceClient(conn),
		caller:    grpcconsumer.New(b, vocabulary),
	}
}

func (c *Client) ListStocks(ctx context.Context, productIDs []string) ([]*results.Stock, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list stocks", func(ctx context.Context) (*inventoryv1.ListStocksResponse, error) {
		return c.inventory.ListStocks(ctx, &inventoryv1.ListStocksRequest{ProductIds: productIDs})
	})
	if err != nil {
		return nil, err
	}
	out := make([]*results.Stock, 0, len(resp.GetStocks()))
	for _, stock := range resp.GetStocks() {
		out = append(out, &results.Stock{ProductID: stock.GetProductId(), Quantity: int(stock.GetQuantity())})
	}
	return out, nil
}

func (c *Client) AdjustStock(ctx context.Context, productID string, delta int, kind, note, actor, reference string) (*results.Stock, *results.Movement, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "adjust stock", func(ctx context.Context) (*inventoryv1.AdjustStockResponse, error) {
		return c.inventory.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
			ProductId: productID, Delta: int32(delta), Kind: kind, Note: note, Actor: actor, Reference: reference,
		})
	})
	if err != nil {
		return nil, nil, err
	}
	mapped := &results.Stock{ProductID: resp.GetStock().GetProductId(), Quantity: int(resp.GetStock().GetQuantity())}
	return mapped, movementFromWire(resp.GetMovement()), nil
}

func (c *Client) ListMovements(ctx context.Context, productID string, limit int) ([]*results.Movement, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list movements", func(ctx context.Context) (*inventoryv1.ListStockMovementsResponse, error) {
		return c.inventory.ListStockMovements(ctx, &inventoryv1.ListStockMovementsRequest{ProductId: productID, PageSize: int32(limit)})
	})
	if err != nil {
		return nil, err
	}
	out := make([]*results.Movement, 0, len(resp.GetMovements()))
	for _, movement := range resp.GetMovements() {
		out = append(out, movementFromWire(movement))
	}
	return out, nil
}

func movementFromWire(movement *inventoryv1.StockMovement) *results.Movement {
	if movement == nil {
		return nil
	}
	return &results.Movement{
		ProductID: movement.GetProductId(),
		Kind:      movement.GetKind(),
		Delta:     int(movement.GetQuantityDelta()),
		Before:    int(movement.GetQuantityBefore()),
		After:     int(movement.GetQuantityAfter()),
		Note:      movement.GetNote(),
		Actor:     movement.GetActor(),
		CreatedAt: asTime(movement.GetCreatedAt()),
	}
}

func asTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
