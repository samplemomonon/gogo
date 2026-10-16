// Package repository_test covers the product-service repository adapters.
//
// The event recorder tests pin the payload bytes of every event type to the
// wire contract shared with product-listing-service and product-search-service,
// which decode these payloads with their own duplicated structs. A rename here
// would not fail their tests, only silently zero a field, so the exact JSON is
// asserted here at the producer.
//
// The ProductPostgres tests are hermetic: they drive the methods with a dead
// DSN and assert the error wrapping, since every statement fails at the
// connection before any SQL runs.
package repository_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"product-service/internal/adapter/repository"
	"product-service/internal/domain/product"
)

func decodePayload(t *testing.T, evt *product.DomainEvent) map[string]json.RawMessage {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("payload is not a JSON object: %v (payload %s)", err, evt.Payload)
	}
	return payload
}

func requireString(t *testing.T, payload map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw, ok := payload[key]
	if !ok {
		t.Fatalf("payload has no %q key: %v", key, payload)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s is not a string: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func requireNumber(t *testing.T, payload map[string]json.RawMessage, key string, want float64) {
	t.Helper()
	raw, ok := payload[key]
	if !ok {
		t.Fatalf("payload has no %q key: %v", key, payload)
	}
	var got float64
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s is not a number: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", key, got, want)
	}
}

func TestRecorderProductCreated(t *testing.T) {
	rec := repository.NewDomainEventRecorder()
	p := &product.Product{
		ID:         7,
		CategoryID: 3,
		Name:       "Kopi",
		Slug:       "kopi",
		Price:      product.Money{Amount: 15_000, Currency: "IDR"},
		Stock:      5,
		Status:     product.StatusActive,
	}
	variant := &product.Variant{
		ID:        11,
		ProductID: 7,
		SKU:       "KOPI-1",
		Name:      "Small",
		Price:     product.Money{Amount: 15_000, Currency: "IDR"},
		Stock:     5,
	}

	events, err := rec.ProductCreated(p, []*product.Variant{variant})
	if err != nil {
		t.Fatalf("ProductCreated: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want one product event plus one per variant", len(events))
	}

	productEvent := events[0]
	if productEvent.EventType != product.EventProductCreated {
		t.Fatalf("EventType = %v", productEvent.EventType)
	}
	if productEvent.AggregateID != 7 || productEvent.PartitionKey != "7" {
		t.Fatalf("aggregate = (%d, %q), want (7, \"7\")", productEvent.AggregateID, productEvent.PartitionKey)
	}
	if productEvent.Topic != "product.events.v1" || productEvent.SchemaVersion != 1 {
		t.Fatalf("envelope fields = (%q, %d)", productEvent.Topic, productEvent.SchemaVersion)
	}

	payload := decodePayload(t, productEvent)
	requireNumber(t, payload, "id", 7)
	requireNumber(t, payload, "category_id", 3)
	requireString(t, payload, "name", "Kopi")
	requireNumber(t, payload, "price_cents", 15000)
	requireString(t, payload, "currency", "IDR")
	requireString(t, payload, "status", "active")

	variantEvent := events[1]
	if variantEvent.EventType != product.EventVariantCreated {
		t.Fatalf("EventType = %v", variantEvent.EventType)
	}
	if variantEvent.AggregateID != 11 {
		t.Fatalf("variant aggregate id = %d, want the variant id 11", variantEvent.AggregateID)
	}
	if variantEvent.PartitionKey != "7" {
		t.Fatalf("variant partition key = %q, want the parent product id 7", variantEvent.PartitionKey)
	}
	variantPayload := decodePayload(t, variantEvent)
	requireNumber(t, variantPayload, "id", 11)
	requireNumber(t, variantPayload, "product_id", 7)
	requireString(t, variantPayload, "sku", "KOPI-1")
	requireNumber(t, variantPayload, "price_cents", 15000)
}

func TestRecorderProductUpdated(t *testing.T) {
	rec := repository.NewDomainEventRecorder()
	p := &product.Product{ID: 7, Name: "Kopi", Price: product.Money{Currency: "IDR"}}

	events, err := rec.ProductUpdated(p)
	if err != nil {
		t.Fatalf("ProductUpdated: %v", err)
	}
	if len(events) != 1 || events[0].EventType != product.EventProductUpdated {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].AggregateID != 7 || events[0].PartitionKey != "7" {
		t.Fatalf("aggregate = (%d, %q)", events[0].AggregateID, events[0].PartitionKey)
	}
	requireString(t, decodePayload(t, events[0]), "name", "Kopi")
}

func TestRecorderProductDeleted(t *testing.T) {
	rec := repository.NewDomainEventRecorder()

	events, err := rec.ProductDeleted(7)
	if err != nil {
		t.Fatalf("ProductDeleted: %v", err)
	}
	if len(events) != 1 || events[0].EventType != product.EventProductDeleted {
		t.Fatalf("unexpected events: %+v", events)
	}
	requireNumber(t, decodePayload(t, events[0]), "id", 7)
}

func TestRecorderProductStockAdjusted(t *testing.T) {
	rec := repository.NewDomainEventRecorder()
	p := &product.Product{ID: 7, Stock: 12}

	events, err := rec.ProductStockAdjusted(p, -3)
	if err != nil {
		t.Fatalf("ProductStockAdjusted: %v", err)
	}
	if len(events) != 1 || events[0].EventType != product.EventProductStockAdjusted {
		t.Fatalf("unexpected events: %+v", events)
	}
	payload := decodePayload(t, events[0])
	requireNumber(t, payload, "id", 7)
	requireNumber(t, payload, "stock", 12)
	requireNumber(t, payload, "delta", -3)
}

func TestRecorderVariantUpdatedAndDeleted(t *testing.T) {
	rec := repository.NewDomainEventRecorder()
	v := &product.Variant{ID: 11, ProductID: 7, SKU: "KOPI-1", Price: product.Money{Currency: "IDR"}}

	events, err := rec.VariantUpdated(v)
	if err != nil {
		t.Fatalf("VariantUpdated: %v", err)
	}
	updated := events[0]
	if updated.EventType != product.EventVariantUpdated || updated.AggregateID != 11 || updated.PartitionKey != "7" {
		t.Fatalf("updated event = %+v", updated)
	}
	requireString(t, decodePayload(t, updated), "sku", "KOPI-1")

	events, err = rec.VariantDeleted(7, 11)
	if err != nil {
		t.Fatalf("VariantDeleted: %v", err)
	}
	deleted := events[0]
	if deleted.EventType != product.EventVariantDeleted || deleted.AggregateID != 11 || deleted.PartitionKey != "7" {
		t.Fatalf("deleted event = %+v", deleted)
	}
	payload := decodePayload(t, deleted)
	requireNumber(t, payload, "product_id", 7)
	requireNumber(t, payload, "variant_id", 11)
}

func TestRecorderEncodesTimesAndNullableFields(t *testing.T) {
	rec := repository.NewDomainEventRecorder()
	p := &product.Product{
		ID:        7,
		Name:      "Kopi",
		Price:     product.Money{Currency: "IDR"},
		CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 2, 11, 30, 0, 0, time.UTC),
	}

	events, err := rec.ProductUpdated(p)
	if err != nil {
		t.Fatalf("ProductUpdated: %v", err)
	}
	payload := decodePayload(t, events[0])
	var createdAt time.Time
	if err := json.Unmarshal(payload["created_at"], &createdAt); err != nil {
		t.Fatalf("created_at is not an RFC 3339 timestamp: %v", err)
	}
	if !createdAt.Equal(p.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", createdAt, p.CreatedAt)
	}
	for _, key := range []string{"description", "brand", "primary_media_id"} {
		if raw, ok := payload[key]; !ok || string(raw) != "null" {
			t.Fatalf("%s = %s (present %v), want null", key, raw, ok)
		}
	}
}

// unreachableDSN points at a closed port, so every statement fails at the
// connection before any SQL runs. connect_timeout keeps the failure quick.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1&sslmode=disable"

func newUnreachableRepo(t *testing.T) *repository.ProductPostgres {
	t.Helper()
	db, err := sql.Open("postgres", unreachableDSN)
	if err != nil {
		t.Fatalf("open database handle: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return repository.NewProductPostgres(db)
}

func TestProductPostgresConnectionFailureIsNotNotFound(t *testing.T) {
	repo := newUnreachableRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tests := []struct {
		name    string
		call    func() error
		wantMsg string
	}{
		{
			name:    "get by id",
			call:    func() error { _, err := repo.GetByID(ctx, 3); return err },
			wantMsg: "get product 3",
		},
		{
			name: "create product",
			call: func() error {
				_, err := repo.CreateProduct(ctx, &product.Product{Name: "x"}, nil)
				return err
			},
			wantMsg: "begin create product",
		},
		{
			name:    "delete product",
			call:    func() error { return repo.DeleteProduct(ctx, 3) },
			wantMsg: "begin delete product 3",
		},
		{
			name:    "adjust stock",
			call:    func() error { _, err := repo.AdjustStock(ctx, 3, -1); return err },
			wantMsg: "begin adjust stock of product 3",
		},
		{
			name:    "get variant",
			call:    func() error { _, err := repo.GetVariantByID(ctx, 11); return err },
			wantMsg: "get variant 11",
		},
		{
			name:    "delete variant",
			call:    func() error { return repo.DeleteVariant(ctx, 3, 11) },
			wantMsg: "delete variant 11",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("expected an error from the dead connection")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantMsg)
			}
			if errors.Is(err, product.ErrProductNotFound) || errors.Is(err, product.ErrVariantNotFound) {
				t.Fatal("a connection failure must never be classified as not found")
			}
		})
	}
}

func TestProductPostgresBeginTxFailure(t *testing.T) {
	repo := newUnreachableRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := repo.BeginTx(ctx); err == nil || !strings.Contains(err.Error(), "begin transaction") {
		t.Fatalf("BeginTx error = %v, want it to mention begin transaction", err)
	}
}
