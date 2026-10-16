// Package domain holds the inventory aggregate: the stock level of a product and
// the append-only movement ledger behind it. It is the only writer of stock.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tenant names the website a stock row belongs to, mirroring the catalog's tenant.
type Tenant string

var (
	ErrStockNotFound = errors.New("stock not found")

	// ErrInsufficientStock is returned when a delta would drive stock below zero.
	ErrInsufficientStock = errors.New("insufficient stock")

	ErrInvalidAdjustment = errors.New("invalid stock adjustment")

	// ErrInvalidTenant is returned when an operation is not scoped to a tenant.
	ErrInvalidTenant = fmt.Errorf("%w: tenant is required", ErrInvalidAdjustment)

	// ErrInvalidProductID is returned when an adjustment targets no product.
	ErrInvalidProductID = fmt.Errorf("%w: product id is required", ErrInvalidAdjustment)
)

// Stock is the current sellable quantity of one product.
type Stock struct {
	ProductID string
	Quantity  int
	UpdatedAt time.Time
}

// Movement is one append-only ledger entry.
type Movement struct {
	ID        string
	ProductID string
	Kind      string
	Delta     int
	Before    int
	After     int
	Note      string
	Actor     string
	Reference string
	CreatedAt time.Time
}

// NewAdjustment validates an adjustment request before it reaches persistence.
func NewAdjustment(tenant Tenant, productID string, kind, actor, reference string) error {
	if strings.TrimSpace(string(tenant)) == "" {
		return ErrInvalidTenant
	}
	if strings.TrimSpace(productID) == "" {
		return ErrInvalidProductID
	}
	return nil
}

// Classify maps an inventory error to a coarse kind the presentation adapters
// project onto their own vocabulary.
type Kind int

const (
	KindInternal Kind = iota
	KindNotFound
	KindInvalid
	KindFailedPrecondition
)

// Classify classifies err.
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindInternal
	case errors.Is(err, ErrStockNotFound):
		return KindNotFound
	case errors.Is(err, ErrInsufficientStock):
		return KindFailedPrecondition
	case errors.Is(err, ErrInvalidAdjustment):
		return KindInvalid
	default:
		return KindInternal
	}
}

// Message returns a stable, client-facing message for err.
func Message(err error) string {
	switch {
	case errors.Is(err, ErrStockNotFound):
		return "stock not found"
	case errors.Is(err, ErrInsufficientStock):
		return "insufficient stock"
	case errors.Is(err, ErrInvalidAdjustment):
		return "invalid stock adjustment"
	default:
		return "something went wrong"
	}
}
