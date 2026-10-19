// Package salesclient is the seller console's adapter over the sales.v1 gRPC contract.
package salesclient

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	salesv1 "gogo/contracts/sales/v1"
	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// The sales vocabulary in the caller's language. Every answer the contract can
// give is named, so a void of an already-voided sale is distinguishable from a
// sale service that is down.
var (
	// ErrTransactionNotFound is returned when the sale named by id does not exist.
	ErrTransactionNotFound = errors.New("transaction not found")
	// ErrInvalidCheckout is returned when the basket itself is rejected.
	ErrInvalidCheckout = errors.New("invalid checkout")
	// ErrSaleRefused is returned when a domain rule refuses the sale, such as a
	// payment that does not cover the total.
	ErrSaleRefused = errors.New("sale refused")
	// ErrSalesUnavailable is returned when the sales service cannot be reached.
	ErrSalesUnavailable = errors.New("sales unavailable")
)

var vocabulary = grpcconsumer.Vocabulary{
	NotFound:     ErrTransactionNotFound,
	Invalid:      ErrInvalidCheckout,
	Precondition: ErrSaleRefused,
	Unavailable:  ErrSalesUnavailable,
}

// Client adapts the generated sales.v1 client to the seller page's ports.
type Client struct {
	sales  salesv1.SalesServiceClient
	caller *grpcconsumer.Caller
}

func New(conn grpc.ClientConnInterface, b *breaker.Breaker) *Client {
	return &Client{
		sales:  salesv1.NewSalesServiceClient(conn),
		caller: grpcconsumer.New(b, vocabulary),
	}
}

func (c *Client) List(ctx context.Context) ([]*results.Transaction, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list transactions", func(ctx context.Context) (*salesv1.ListTransactionsResponse, error) {
		return c.sales.ListTransactions(ctx, &salesv1.ListTransactionsRequest{PageSize: 100})
	})
	if err != nil {
		return nil, err
	}
	out := make([]*results.Transaction, 0, len(resp.GetTransactions()))
	for _, transaction := range resp.GetTransactions() {
		out = append(out, fromWire(transaction))
	}
	return out, nil
}

func (c *Client) Checkout(ctx context.Context, items []*results.TransactionItem, discount, paid int64, method, cashier, employeeID string) (*results.Transaction, error) {
	wireItems := make([]*salesv1.CheckoutItem, 0, len(items))
	for _, item := range items {
		wireItems = append(wireItems, &salesv1.CheckoutItem{
			ProductId: item.ProductID,
			Name:      item.Name,
			Quantity:  int32(item.Quantity),
			Price:     item.Price,
			Discount:  item.Discount,
		})
	}

	resp, err := grpcconsumer.Do(c.caller, ctx, "check out", func(ctx context.Context) (*salesv1.CheckoutResponse, error) {
		return c.sales.Checkout(ctx, &salesv1.CheckoutRequest{
			Items: wireItems, Discount: discount, Paid: paid,
			PaymentMethod: method, Cashier: cashier, EmployeeId: employeeID,
		})
	})
	if err != nil {
		return nil, err
	}
	return fromWire(resp.GetTransaction()), nil
}

func (c *Client) Void(ctx context.Context, id, by, reason string) (*results.Transaction, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "void transaction", func(ctx context.Context) (*salesv1.VoidTransactionResponse, error) {
		return c.sales.VoidTransaction(ctx, &salesv1.VoidTransactionRequest{Id: id, VoidedBy: by, Reason: reason})
	})
	if err != nil {
		return nil, err
	}
	return fromWire(resp.GetTransaction()), nil
}

func fromWire(transaction *salesv1.Transaction) *results.Transaction {
	if transaction == nil {
		return nil
	}
	items := make([]*results.TransactionItem, 0, len(transaction.GetItems()))
	for _, item := range transaction.GetItems() {
		items = append(items, &results.TransactionItem{
			ProductID: item.GetProductId(),
			Name:      item.GetName(),
			Quantity:  int(item.GetQuantity()),
			Price:     item.GetPrice(),
			Discount:  item.GetDiscount(),
			Subtotal:  item.GetSubtotal(),
		})
	}

	out := &results.Transaction{
		ID:            transaction.GetId(),
		Number:        transaction.GetNumber(),
		Total:         transaction.GetTotal(),
		Discount:      transaction.GetDiscount(),
		Paid:          transaction.GetPaid(),
		Change:        transaction.GetChange(),
		PaymentMethod: transaction.GetPaymentMethod(),
		Cashier:       transaction.GetCashier(),
		EmployeeID:    transaction.GetEmployeeId(),
		CreatedAt:     asTime(transaction.GetCreatedAt()),
		VoidedBy:      transaction.GetVoidedBy(),
		VoidReason:    transaction.GetVoidReason(),
		Items:         items,
	}
	if voidedAt := transaction.GetVoidedAt(); voidedAt != nil {
		voided := voidedAt.AsTime()
		out.VoidedAt = &voided
	}
	return out
}

func asTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
