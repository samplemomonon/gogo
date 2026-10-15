package product

import (
	"strings"
)

// Currency is an ISO 4217 currency code.
type Currency string

// NewCurrency parses and normalizes a currency code.
func NewCurrency(s string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(s)))
	if err := validateCurrency(string(c)); err != nil {
		return "", err
	}
	return c, nil
}

// String returns the currency code.
func (c Currency) String() string { return string(c) }

// Money is a monetary amount in minor units paired with its currency.
type Money struct {
	Amount   int64
	Currency Currency
}

// NewMoney creates a validated Money value.
func NewMoney(amount int64, currency Currency) (Money, error) {
	m := Money{Amount: amount, Currency: currency}
	if err := m.validate(); err != nil {
		return Money{}, err
	}
	return m, nil
}

func (m Money) validate() error {
	if m.Amount < 0 {
		return ErrNegativePrice
	}
	if err := validateCurrency(string(m.Currency)); err != nil {
		return err
	}
	return nil
}
