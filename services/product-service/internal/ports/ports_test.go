// Package ports_test pins the product-service port contract: every adapter
// that is wired as a port implementation must satisfy it, and the ports
// themselves must stay narrow enough to fake in use case tests.
//
// The assertions are compile-time on purpose. They fail the moment an adapter
// method signature drifts from its interface — for example while refactoring
// an error path — rather than at runtime in the composition root.
package ports_test

import (
	"context"
	"testing"

	productgrpc "product-service/internal/adapter/grpc"
	productmessaging "product-service/internal/adapter/messaging"
	productrepository "product-service/internal/adapter/repository"
	"product-service/internal/ports"
)

// The repository is the production implementation of the persistence and
// in-transaction event ports.
var (
	_ ports.ProductRepository  = (*productrepository.ProductPostgres)(nil)
	_ ports.VariantRepository  = (*productrepository.ProductPostgres)(nil)
	_ ports.EventSink          = (*productrepository.ProductPostgres)(nil)
	_ ports.EventRecorder      = (*productrepository.DomainEventRecorder)(nil)
	_ ports.OutboxEventRecorder = (*productmessaging.OutboxRecorder)(nil)
)

// The gRPC clients are the production implementations of the reference lookups.
var (
	_ ports.CategoryLookup = (*productgrpc.CategoryClient)(nil)
	_ ports.MediaLookup    = (*productgrpc.MediaClient)(nil)
)

// TestReferenceLookupsAreInterchangeable pins at runtime that the category and
// media lookups share one contract. The use cases hold them as two separate
// ports today, so the two fake implementations in the use case tests stand in
// for the same shape: a map from requested id to "exists".
func TestReferenceLookupsAreInterchangeable(t *testing.T) {
	t.Parallel()

	fake := fakeLookup{1: true}
	var category ports.CategoryLookup = fake
	var media ports.MediaLookup = fake

	got, err := category.Exists(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("category lookup: %v", err)
	}
	if !got[1] || got[2] {
		t.Fatalf("category lookup = %v, want {1: true, 2: false}", got)
	}

	got, err = media.Exists(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("media lookup: %v", err)
	}
	if !got[1] || got[2] {
		t.Fatalf("media lookup = %v, want {1: true, 2: false}", got)
	}
}

type fakeLookup map[int64]bool

func (f fakeLookup) Exists(_ context.Context, ids []int64) (map[int64]bool, error) {
	exists := make(map[int64]bool, len(ids))
	for _, id := range ids {
		exists[id] = f[id]
	}
	return exists, nil
}
