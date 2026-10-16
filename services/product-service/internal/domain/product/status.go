// Package product contains the core domain models and business rules for products.
package product

import (
	"fmt"
	"strings"
)

// Status represents the publication state of a product.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// ParseStatus converts a string to a Status, rejecting unknown values.
func ParseStatus(s string) (Status, error) {
	st := Status(strings.ToLower(strings.TrimSpace(s)))
	switch st {
	case StatusDraft, StatusActive, StatusArchived:
		return st, nil
	case "":
		return StatusDraft, nil
	default:
		return "", fmt.Errorf("unknown product status %q, want draft|active|archived", s)
	}
}

// String returns the string representation of the status.
func (s Status) String() string {
	return string(s)
}

// IsValid checks if the status is a valid value.
func (s Status) IsValid() bool {
	switch s {
	case StatusDraft, StatusActive, StatusArchived:
		return true
	default:
		return false
	}
}
