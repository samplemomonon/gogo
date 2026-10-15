// Package domain holds the sales aggregate: completed point-of-sale transactions and
// their line items. It computes totals server-side so a client cannot underpay.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tenant names the website a transaction belongs to.
type Tenant string

var (
	ErrTransactionNotFound = errors.New("transaction not found")
	ErrAlreadyVoided       = errors.New("transaction already voided")
	ErrInsufficientStock   = errors.New("insufficient stock for a sale line")
	ErrProductNotFound     = errors.New("product not found for a sale line")

	ErrInvalidTransaction  = errors.New("invalid transaction")
	ErrInvalidTenant       = fmt.Errorf("%w: tenant is required", ErrInvalidTransaction)
	ErrInvalidID           = fmt.Errorf("%w: id is required", ErrInvalidTransaction)
	ErrNoItems             = fmt.Errorf("%w: at least one item is required", ErrInvalidTransaction)
	ErrInvalidItem         = fmt.Errorf("%w: item quantity and price must not be negative", ErrInvalidTransaction)
	ErrInsufficientPayment = fmt.Errorf("%w: paid is less than the total", ErrInvalidTransaction)
)

// Kind classifies a sales error for the presentation adapters.
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
	case errors.Is(err, ErrTransactionNotFound):
		return KindNotFound
	case errors.Is(err, ErrAlreadyVoided), errors.Is(err, ErrInsufficientPayment), errors.Is(err, ErrInsufficientStock):
		return KindFailedPrecondition
	case errors.Is(err, ErrProductNotFound), errors.Is(err, ErrInvalidTransaction):
		return KindInvalid
	default:
		return KindInternal
	}
}

// Message returns a stable, client-facing message for err.
func Message(err error) string {
	switch {
	case errors.Is(err, ErrTransactionNotFound):
		return "transaction not found"
	case errors.Is(err, ErrAlreadyVoided):
		return "transaction already voided"
	case errors.Is(err, ErrInsufficientStock):
		return "insufficient stock for a sale line"
	case errors.Is(err, ErrProductNotFound):
		return "product not found for a sale line"
	case errors.Is(err, ErrInsufficientPayment):
		return "paid is less than the total"
	case errors.Is(err, ErrInvalidTransaction):
		return "invalid transaction"
	default:
		return "something went wrong"
	}
}

// CheckoutItem is a requested line before totals are computed.
type CheckoutItem struct {
	ProductID string
	Name      string
	Quantity  int
	Price     int64
	Discount  int64
}

// Item is a stored line with its computed subtotal.
type Item struct {
	ID        string
	ProductID string
	Name      string
	Quantity  int
	Price     int64
	Discount  int64
	Subtotal  int64
}

// Transaction is one completed sale.
type Transaction struct {
	TenantID      Tenant
	ID            string
	Number        string
	Total         int64
	Discount      int64
	Paid          int64
	Change        int64
	PaymentMethod string
	Cashier       string
	EmployeeID    string
	CreatedAt     time.Time
	VoidedAt      *time.Time
	VoidedBy      string
	VoidReason    string
	Items         []Item
}

// NewTransaction validates a checkout and computes its line subtotals, total and
// change. A checkout with no items, a negative quantity/price/discount or a
// payment below the total is rejected.
func NewTransaction(tenant Tenant, id, number string, requested []CheckoutItem, discount, paid int64, method, cashier, employeeID string) (*Transaction, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	method = strings.TrimSpace(method)
	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidID
	}
	if len(requested) == 0 {
		return nil, ErrNoItems
	}
	if discount < 0 {
		return nil, ErrInvalidItem
	}

	items := make([]Item, 0, len(requested))
	var total int64
	for _, line := range requested {
		if line.Quantity <= 0 || line.Price < 0 || line.Discount < 0 || line.Discount > line.Price*int64(line.Quantity) {
			return nil, ErrInvalidItem
		}
		subtotal := line.Price*int64(line.Quantity) - line.Discount
		total += subtotal
		items = append(items, Item{
			ProductID: line.ProductID,
			Name:      strings.TrimSpace(line.Name),
			Quantity:  line.Quantity,
			Price:     line.Price,
			Discount:  line.Discount,
			Subtotal:  subtotal,
		})
	}

	total -= discount
	if total < 0 {
		total = 0
	}
	if paid < total {
		return nil, ErrInsufficientPayment
	}
	if method == "" {
		method = "Tunai"
	}

	return &Transaction{
		TenantID:      tenant,
		ID:            id,
		Number:        number,
		Total:         total,
		Discount:      discount,
		Paid:          paid,
		Change:        paid - total,
		PaymentMethod: method,
		Cashier:       cashier,
		EmployeeID:    employeeID,
		CreatedAt:     time.Now(),
		Items:         items,
	}, nil
}

// Void stamps the transaction voided. A second void is rejected.
func (t *Transaction) Void(by, reason string) error {
	if t.VoidedAt != nil {
		return ErrAlreadyVoided
	}
	now := time.Now()
	t.VoidedAt = &now
	t.VoidedBy = by
	t.VoidReason = reason
	return nil
}
