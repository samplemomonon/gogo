package product

import (
	"testing"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		input   string
		want    Status
		wantErr bool
	}{
		{"draft", StatusDraft, false},
		{"active", StatusActive, false},
		{"archived", StatusArchived, false},
		{"  DRAFT  ", StatusDraft, false},
		{"Active", StatusActive, false},
		{"", StatusDraft, false},
		{"unknown", "", true},
		{"deleted", "", true},
	}
	for _, tt := range tests {
		got, err := ParseStatus(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseStatus(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseStatus(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestStatusIsValid(t *testing.T) {
	if !StatusDraft.IsValid() || !StatusActive.IsValid() || !StatusArchived.IsValid() {
		t.Error("valid statuses should be valid")
	}
	if Status("unknown").IsValid() {
		t.Error("unknown status should not be valid")
	}
}

func TestNewProduct(t *testing.T) {
	p, err := NewProduct(1, "Test Product", "test-product", nil, nil, mustMoney(1000, "USD"), 10, StatusDraft, nil)
	if err != nil {
		t.Fatalf("NewProduct: %v", err)
	}
	if p.Name != "Test Product" {
		t.Errorf("Name = %q, want %q", p.Name, "Test Product")
	}
	if p.Slug != "test-product" {
		t.Errorf("Slug = %q, want %q", p.Slug, "test-product")
	}
	if p.Price.Currency != "USD" {
		t.Errorf("Currency = %q, want %q", p.Price.Currency, "USD")
	}
	if p.Status != StatusDraft {
		t.Errorf("Status = %v, want %v", p.Status, StatusDraft)
	}
}

func TestNewProductValidation(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
		input   func() (*Product, error)
	}{
		{"empty name", ErrEmptyName, func() (*Product, error) {
			return NewProduct(1, "", "slug", nil, nil, mustMoney(0, "USD"), 0, StatusDraft, nil)
		}},
		{"name too long", ErrNameTooLong, func() (*Product, error) {
			return NewProduct(1, string(make([]byte, 300)), "slug", nil, nil, mustMoney(0, "USD"), 0, StatusDraft, nil)
		}},
		{"negative price", ErrNegativePrice, func() (*Product, error) {
			return NewProduct(1, "Name", "slug", nil, nil, Money{Amount: -1, Currency: "USD"}, 0, StatusDraft, nil)
		}},
		{"negative stock", ErrNegativeStock, func() (*Product, error) {
			return NewProduct(1, "Name", "slug", nil, nil, mustMoney(0, "USD"), -1, StatusDraft, nil)
		}},
		{"invalid currency", ErrInvalidCurrency, func() (*Product, error) {
			return NewProduct(1, "Name", "slug", nil, nil, Money{Amount: 0, Currency: "US"}, 0, StatusDraft, nil)
		}},
		{"invalid category", ErrInvalidInput, func() (*Product, error) {
			return NewProduct(0, "Name", "slug", nil, nil, mustMoney(0, "USD"), 0, StatusDraft, nil)
		}},
		{"invalid slug chars", ErrInvalidSlug, func() (*Product, error) {
			return NewProduct(1, "Name", "Invalid Slug!", nil, nil, mustMoney(0, "USD"), 0, StatusDraft, nil)
		}},
		{"slug with spaces", ErrInvalidSlug, func() (*Product, error) {
			return NewProduct(1, "Name", "invalid slug", nil, nil, mustMoney(0, "USD"), 0, StatusDraft, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			vErr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if vErr.Err != tt.wantErr {
				t.Errorf("error = %v, want %v", vErr.Err, tt.wantErr)
			}
		})
	}
}

func TestProductUpdate(t *testing.T) {
	p, _ := NewProduct(1, "Old Name", "old-slug", nil, nil, mustMoney(100, "USD"), 5, StatusDraft, nil)

	req := NewUpdateProductRequest(UpdateFields{Name: ptr("New Name"), PriceCents: ptr(int64(200))})
	if err := p.Update(req); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p.Name != "New Name" {
		t.Errorf("Name = %q, want %q", p.Name, "New Name")
	}
	if p.Price.Amount != 200 {
		t.Errorf("Price.Amount = %d, want %d", p.Price.Amount, 200)
	}
	if p.Price.Currency != "USD" {
		t.Errorf("Currency should be unchanged, got %q", p.Price.Currency)
	}
}

func TestProductAdjustStock(t *testing.T) {
	p, _ := NewProduct(1, "Test", "test", nil, nil, mustMoney(100, "USD"), 10, StatusDraft, nil)

	if err := p.AdjustStock(5); err != nil {
		t.Fatalf("AdjustStock(5): %v", err)
	}
	if p.Stock != 15 {
		t.Errorf("Stock = %d, want %d", p.Stock, 15)
	}

	if err := p.AdjustStock(-5); err != nil {
		t.Fatalf("AdjustStock(-5): %v", err)
	}
	if p.Stock != 10 {
		t.Errorf("Stock = %d, want %d", p.Stock, 10)
	}

	if err := p.AdjustStock(-100); err != ErrNegativeStock {
		t.Errorf("AdjustStock(-100) error = %v, want %v", err, ErrNegativeStock)
	}
}

func TestProductCanAddVariant(t *testing.T) {
	p := &Product{}
	if err := p.CanAddVariant(); err != nil {
		t.Errorf("CanAddVariant() should succeed")
	}
	p.Variants = make([]*Variant, MaxVariantsPerProduct)
	if err := p.CanAddVariant(); err != ErrMaxVariantsExceeded {
		t.Errorf("CanAddVariant() error = %v, want %v", err, ErrMaxVariantsExceeded)
	}
}

func TestNewVariant(t *testing.T) {
	v, err := NewVariant(1, "SKU-001", "Variant Name", mustMoney(500, "USD"), 3, nil)
	if err != nil {
		t.Fatalf("NewVariant: %v", err)
	}
	if v.SKU != "SKU-001" {
		t.Errorf("SKU = %q, want %q", v.SKU, "SKU-001")
	}
	if v.Name != "Variant Name" {
		t.Errorf("Name = %q, want %q", v.Name, "Variant Name")
	}
}

func TestNewVariantDefaults(t *testing.T) {
	v, err := NewVariant(1, "SKU-001", "", mustMoney(500, "USD"), 3, nil)
	if err != nil {
		t.Fatalf("NewVariant: %v", err)
	}
	if v.Name != "SKU-001" {
		t.Errorf("Name should default to SKU, got %q", v.Name)
	}
}

func TestVariantUpdate(t *testing.T) {
	v, _ := NewVariant(1, "SKU-001", "Old", mustMoney(100, "USD"), 5, nil)
	req := NewUpdateVariantRequest(ptr("SKU-002"), ptr("New"), nil, nil, nil, nil)
	if err := v.Update(req); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if v.SKU != "SKU-002" {
		t.Errorf("SKU = %q, want %q", v.SKU, "SKU-002")
	}
	if v.Price.Amount != 100 {
		t.Errorf("Price.Amount should be unchanged, got %d", v.Price.Amount)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"  Spaced  Out  ", "spaced-out"},
		{"UPPERCASE", "uppercase"},
		{"with-dashes", "with-dashes"},
		{"special!@#chars", "special-chars"},
		{"  -leading and trailing-  ", "leading-and-trailing"},
		{"multiple---dashes", "multiple-dashes"},
		{"numbers123", "numbers123"},
	}
	for _, tt := range tests {
		if got := Slugify(tt.input); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestValidateCurrency(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"USD", false},
		{"usd", false},
		{"IDR", false},
		{"", true},
		{"US", true},
		{"USDD", true},
		{"U$D", true},
	}
	for _, tt := range tests {
		err := ValidateCurrency(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateCurrency(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"valid-slug", false},
		{"slug-123", false},
		{"", true},
		{"Invalid", true},
		{"with spaces", true},
		{"-leading", true},
		{"trailing-", true},
		{"under_score", true},
	}
	for _, tt := range tests {
		err := ValidateSlug(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateSlug(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
	}
}

func ptr[T any](v T) *T {
	return &v
}

func mustMoney(amount int64, currency Currency) Money {
	return Money{Amount: amount, Currency: currency}
}
