package results

import "time"

// TransactionItem is one line of a sale in the console's read model.
type TransactionItem struct {
	ProductID string
	Name      string
	Quantity  int
	Price     int64
	Discount  int64
	Subtotal  int64
}

// Transaction is a completed sale in the console's read model.
type Transaction struct {
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
	Items         []*TransactionItem
}
