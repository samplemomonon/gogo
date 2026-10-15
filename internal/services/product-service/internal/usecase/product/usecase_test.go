package product

import (
	"context"
	"testing"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// Mock repositories for testing use cases.
type mockProductRepo struct {
	CreateFunc func(ctx context.Context, p *product.Product, v []*product.Variant) (*product.Product, error)
	GetByIDRet *product.Product
	GetByIDErr error
	UpdateRet  *product.Product
	UpdateErr  error
	DeleteErr  error
	AdjustRet  *product.Product
	AdjustErr  error
	BeginTxRet ports.Transaction
}

func (m *mockProductRepo) GetByID(ctx context.Context, id int64) (*product.Product, error) {
	return m.GetByIDRet, m.GetByIDErr
}
func (m *mockProductRepo) GetByIDWithVariants(ctx context.Context, id int64) (*product.Product, error) {
	return m.GetByIDRet, m.GetByIDErr
}
func (m *mockProductRepo) ExistsByID(ctx context.Context, id int64) (bool, error) {
	return m.GetByIDRet != nil, nil
}
func (m *mockProductRepo) CreateProduct(ctx context.Context, p *product.Product, v []*product.Variant) (*product.Product, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, p, v)
	}
	return p, nil
}
func (m *mockProductRepo) UpdateProduct(ctx context.Context, p *product.Product) (*product.Product, error) {
	return m.UpdateRet, m.UpdateErr
}
func (m *mockProductRepo) DeleteProduct(ctx context.Context, id int64) error {
	return m.DeleteErr
}
func (m *mockProductRepo) AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error) {
	return m.AdjustRet, m.AdjustErr
}
func (m *mockProductRepo) BeginTx(ctx context.Context) (ports.Transaction, error) {
	return m.BeginTxRet, nil
}

type mockVariantRepo struct {
	GetVariantByIDRet *product.Variant
	GetVariantByIDErr error
	CountRet          int
	CountErr          error
	CreateRet         *product.Variant
	CreateErr         error
	UpdateRet         *product.Variant
	UpdateErr         error
	DeleteErr         error
}

func (m *mockVariantRepo) GetVariantByID(ctx context.Context, id int64) (*product.Variant, error) {
	return m.GetVariantByIDRet, m.GetVariantByIDErr
}
func (m *mockVariantRepo) CountByProductID(ctx context.Context, productID int64) (int, error) {
	return m.CountRet, m.CountErr
}
func (m *mockVariantRepo) CreateVariant(ctx context.Context, v *product.Variant) (*product.Variant, error) {
	return m.CreateRet, m.CreateErr
}
func (m *mockVariantRepo) UpdateVariant(ctx context.Context, v *product.Variant) (*product.Variant, error) {
	return m.UpdateRet, m.UpdateErr
}
func (m *mockVariantRepo) DeleteVariant(ctx context.Context, productID, variantID int64) error {
	return m.DeleteErr
}

type mockCategoryLookup struct {
	ExistsRet map[int64]bool
	ExistsErr error
}

func (m *mockCategoryLookup) Exists(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return m.ExistsRet, m.ExistsErr
}

type mockMediaLookup struct {
	ExistsRet map[int64]bool
	ExistsErr error
}

func (m *mockMediaLookup) Exists(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return m.ExistsRet, m.ExistsErr
}

type mockEventRecorder struct{}

func (m *mockEventRecorder) ProductCreated(p *product.Product, v []*product.Variant) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) ProductUpdated(p *product.Product) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) ProductDeleted(id int64) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) ProductStockAdjusted(p *product.Product, d int64) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) VariantCreated(v *product.Variant) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) VariantUpdated(v *product.Variant) ([]*product.DomainEvent, error) {
	return nil, nil
}
func (m *mockEventRecorder) VariantDeleted(pid, vid int64) ([]*product.DomainEvent, error) {
	return nil, nil
}

type mockEventSink struct{}

func (m *mockEventSink) Append(ctx context.Context, tx ports.Transaction, evts []*product.DomainEvent) ([]*product.DomainEvent, error) {
	return evts, nil
}

type mockOutboxRecorder struct{}

func (m *mockOutboxRecorder) Append(ctx context.Context, evt *product.DomainEvent) error {
	return nil
}

type mockKafkaPublisher struct{}

func (m *mockKafkaPublisher) Append(ctx context.Context, evt *product.DomainEvent) error {
	return nil
}

type mockTx struct{}

func (m *mockTx) Commit() error   { return nil }
func (m *mockTx) Rollback() error { return nil }

func TestCreateProductUseCase_Execute(t *testing.T) {
	repo := &mockProductRepo{
		CreateFunc: func(ctx context.Context, p *product.Product, v []*product.Variant) (*product.Product, error) {
			p.ID = 1
			return p, nil
		},
		BeginTxRet: &mockTx{},
	}
	uc := NewCreateProductUseCase(UseCaseDependencies{
		ProductRepo:     repo,
		VariantRepo:     &mockVariantRepo{},
		CategoryLookup:  &mockCategoryLookup{ExistsRet: map[int64]bool{1: true}},
		MediaLookup:     &mockMediaLookup{ExistsRet: map[int64]bool{1: true}},
		EventRecorder:   &mockEventRecorder{},
		EventSink:       &mockEventSink{},
		KafkaPublisher:  &mockKafkaPublisher{},
		OutboxRecorder:  &mockOutboxRecorder{},
		DefaultCurrency: "USD",
	})

	p, err := uc.Execute(context.Background(), &product.CreateProductRequest{
		CategoryID: 1,
		Name:       "Test Product",
		Slug:       "test-product",
		PriceCents: 100,
		Currency:   "USD",
		Stock:      10,
		Status:     product.StatusDraft,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p.ID != 1 {
		t.Errorf("ID = %d, want 1", p.ID)
	}
	if p.Name != "Test Product" {
		t.Errorf("Name = %q, want %q", p.Name, "Test Product")
	}
}

func TestCreateProductUseCase_InvalidCategory(t *testing.T) {
	repo := &mockProductRepo{BeginTxRet: &mockTx{}}
	uc := NewCreateProductUseCase(UseCaseDependencies{
		ProductRepo:     repo,
		VariantRepo:     &mockVariantRepo{},
		CategoryLookup:  &mockCategoryLookup{ExistsRet: map[int64]bool{}},
		MediaLookup:     &mockMediaLookup{},
		EventRecorder:   &mockEventRecorder{},
		EventSink:       &mockEventSink{},
		KafkaPublisher:  &mockKafkaPublisher{},
		OutboxRecorder:  &mockOutboxRecorder{},
		DefaultCurrency: "USD",
	})

	_, err := uc.Execute(context.Background(), &product.CreateProductRequest{
		CategoryID: 999,
		Name:       "Test",
		Slug:       "test",
		PriceCents: 100,
		Status:     product.StatusDraft,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent category")
	}
}

func TestUpdateProductUseCase_Execute(t *testing.T) {
	existing := &product.Product{
		ID:         1,
		CategoryID: 1,
		Name:       "Old Name",
		Slug:       "old-name",
		Price:      product.Money{Amount: 100, Currency: "USD"},
		Stock:      5,
		Status:     product.StatusDraft,
	}
	repo := &mockProductRepo{
		GetByIDRet: existing,
		UpdateRet:  existing,
		BeginTxRet: &mockTx{},
	}
	uc := NewUpdateProductUseCase(UseCaseDependencies{
		ProductRepo:    repo,
		VariantRepo:    &mockVariantRepo{},
		CategoryLookup: &mockCategoryLookup{ExistsRet: map[int64]bool{1: true}},
		MediaLookup:    &mockMediaLookup{},
		EventRecorder:  &mockEventRecorder{},
		EventSink:      &mockEventSink{},
		KafkaPublisher: &mockKafkaPublisher{},
		OutboxRecorder: &mockOutboxRecorder{},
	})

	updated, err := uc.Execute(context.Background(), 1, product.NewUpdateProductRequest(product.UpdateFields{
		Name: ptr("New Name"),
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if updated.Name != "New Name" {
		t.Errorf("Name = %q, want %q", updated.Name, "New Name")
	}
}

func TestAdjustStockUseCase_Execute(t *testing.T) {
	existing := &product.Product{ID: 1, Name: "X", Stock: 10}
	repo := &mockProductRepo{
		AdjustRet:  existing,
		BeginTxRet: &mockTx{},
	}
	uc := NewAdjustStockUseCase(UseCaseDependencies{
		ProductRepo:    repo,
		VariantRepo:    &mockVariantRepo{},
		EventRecorder:  &mockEventRecorder{},
		EventSink:      &mockEventSink{},
		OutboxRecorder: &mockOutboxRecorder{},
	})

	_, err := uc.Execute(context.Background(), 1, 5)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestAdjustStockUseCase_ZeroDelta(t *testing.T) {
	uc := NewAdjustStockUseCase(UseCaseDependencies{
		ProductRepo: &mockProductRepo{BeginTxRet: &mockTx{}},
	})

	_, err := uc.Execute(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("expected error for zero delta")
	}
}

func TestCreateVariantUseCase_Limit(t *testing.T) {
	full := &product.Product{ID: 1, Price: product.Money{Amount: 0, Currency: "USD"}, Variants: make([]*product.Variant, product.MaxVariantsPerProduct)}
	repo := &mockProductRepo{
		GetByIDRet: full,
		BeginTxRet: &mockTx{},
	}
	uc := NewCreateVariantUseCase(UseCaseDependencies{
		ProductRepo:    repo,
		VariantRepo:    &mockVariantRepo{},
		EventRecorder:  &mockEventRecorder{},
		EventSink:      &mockEventSink{},
		OutboxRecorder: &mockOutboxRecorder{},
	})

	_, err := uc.Execute(context.Background(), 1, &product.CreateVariantRequest{
		SKU: "SKU-1", PriceCents: 100, Stock: 5,
	})
	if err == nil {
		t.Fatal("expected error when variant limit reached")
	}
}

func ptr[T any](v T) *T { return &v }
