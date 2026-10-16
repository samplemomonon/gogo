package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewAdjustment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tenant    Tenant
		productID string
		want      error
	}{
		{
			name:      "accepts a scoped adjustment",
			tenant:    "acme",
			productID: "p-1",
			want:      nil,
		},
		{
			name:      "rejects a missing tenant",
			tenant:    "",
			productID: "p-1",
			want:      ErrInvalidTenant,
		},
		{
			// Whitespace is not a tenant: it trims to the empty string, and a
			// row scoped to " " would be a row scoped to nothing.
			name:      "rejects a whitespace-only tenant",
			tenant:    "   ",
			productID: "p-1",
			want:      ErrInvalidTenant,
		},
		{
			name:      "rejects a missing product",
			tenant:    "acme",
			productID: "",
			want:      ErrInvalidProductID,
		},
		{
			name:      "rejects a whitespace-only product",
			tenant:    "acme",
			productID: " \t",
			want:      ErrInvalidProductID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := NewAdjustment(tc.tenant, tc.productID, "receive", "kasir", "ref-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("NewAdjustment = %v, want %v", err, tc.want)
			}
			// The two specific errors are wrapped in ErrInvalidAdjustment so a
			// caller can catch the family or the case — assert the family too.
			if tc.want != nil && !errors.Is(err, ErrInvalidAdjustment) {
				t.Errorf("NewAdjustment = %v, want an ErrInvalidAdjustment", err)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want Kind
	}{
		// nil is not a success kind. The zero value is KindInternal precisely
		// so an unclassified call fails closed to "something went wrong".
		{name: "nil classifies as internal", err: nil, want: KindInternal},
		{name: "missing stock is not found", err: ErrStockNotFound, want: KindNotFound},
		{name: "insufficient stock is a failed precondition", err: ErrInsufficientStock, want: KindFailedPrecondition},
		{name: "a bad request is invalid", err: ErrInvalidAdjustment, want: KindInvalid},
		{name: "a bad tenant is invalid", err: ErrInvalidTenant, want: KindInvalid},
		{name: "a bad product is invalid", err: ErrInvalidProductID, want: KindInvalid},
		{name: "an unknown error is internal", err: errors.New("boom"), want: KindInternal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	// The sentinels travel wrapped through the application and transport
	// layers; classification must survive the wrap or the presentation
	// adapters lose the mapping at exactly the depth it matters.
	t.Run("classifies wrapped sentinels", func(t *testing.T) {
		t.Parallel()

		wrapped := fmt.Errorf("adjust stock: %w", ErrInsufficientStock)
		if got := Classify(wrapped); got != KindFailedPrecondition {
			t.Errorf("Classify(wrapped insufficient stock) = %v, want %v", got, KindFailedPrecondition)
		}
	})
}

func TestMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing stock", err: ErrStockNotFound, want: "stock not found"},
		{name: "insufficient stock", err: ErrInsufficientStock, want: "insufficient stock"},
		{name: "invalid adjustment", err: ErrInvalidAdjustment, want: "invalid stock adjustment"},
		{name: "invalid tenant", err: ErrInvalidTenant, want: "invalid stock adjustment"},
		{name: "invalid product", err: ErrInvalidProductID, want: "invalid stock adjustment"},
		{name: "unknown error", err: errors.New("driver: connection refused"), want: "something went wrong"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := Message(tc.err); got != tc.want {
				t.Errorf("Message(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}

	// The message is client-facing: an internal error must not leak its text.
	t.Run("does not leak internal error detail", func(t *testing.T) {
		t.Parallel()

		if got := Message(errors.New("pq: deadlock detected")); got == "pq: deadlock detected" {
			t.Errorf("Message leaked the internal error text: %q", got)
		}
	})
}
