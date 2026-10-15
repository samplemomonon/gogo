package specification_test

import (
	"testing"

	"gogo/internal/shared/foundation/specification"
)

type Product struct {
	Name     string
	Price    float64
	Category string
	InStock  bool
	Rating   float64
}

func TestSpecification_IsSatisfiedBy(t *testing.T) {
	inStockSpec := specification.NewSpecification(func(p Product) bool {
		return p.InStock
	})

	product1 := Product{Name: "Laptop", InStock: true}
	product2 := Product{Name: "Mouse", InStock: false}

	if !inStockSpec.IsSatisfiedBy(product1) {
		t.Error("expected product1 to satisfy in-stock spec")
	}

	if inStockSpec.IsSatisfiedBy(product2) {
		t.Error("expected product2 to not satisfy in-stock spec")
	}
}

func TestSpecification_And(t *testing.T) {
	inStockSpec := specification.NewSpecification(func(p Product) bool {
		return p.InStock
	})

	affordableSpec := specification.NewSpecification(func(p Product) bool {
		return p.Price <= 100.0
	})

	combined := inStockSpec.And(affordableSpec)

	product1 := Product{Price: 50.0, InStock: true}
	product2 := Product{Price: 150.0, InStock: true}
	product3 := Product{Price: 50.0, InStock: false}

	if !combined.IsSatisfiedBy(product1) {
		t.Error("expected product1 to satisfy combined spec")
	}

	if combined.IsSatisfiedBy(product2) {
		t.Error("expected product2 to not satisfy (price too high)")
	}

	if combined.IsSatisfiedBy(product3) {
		t.Error("expected product3 to not satisfy (not in stock)")
	}
}

func TestSpecification_Or(t *testing.T) {
	highRatedSpec := specification.NewSpecification(func(p Product) bool {
		return p.Rating >= 4.5
	})

	cheapSpec := specification.NewSpecification(func(p Product) bool {
		return p.Price <= 20.0
	})

	combined := highRatedSpec.Or(cheapSpec)

	product1 := Product{Price: 10.0, Rating: 3.0}  // cheap
	product2 := Product{Price: 100.0, Rating: 5.0} // high rated
	product3 := Product{Price: 100.0, Rating: 3.0} // neither

	if !combined.IsSatisfiedBy(product1) {
		t.Error("expected product1 to satisfy (cheap)")
	}

	if !combined.IsSatisfiedBy(product2) {
		t.Error("expected product2 to satisfy (high rated)")
	}

	if combined.IsSatisfiedBy(product3) {
		t.Error("expected product3 to not satisfy")
	}
}

func TestSpecification_Not(t *testing.T) {
	inStockSpec := specification.NewSpecification(func(p Product) bool {
		return p.InStock
	})

	outOfStockSpec := inStockSpec.Not()

	product1 := Product{InStock: true}
	product2 := Product{InStock: false}

	if outOfStockSpec.IsSatisfiedBy(product1) {
		t.Error("expected product1 to not satisfy out-of-stock spec")
	}

	if !outOfStockSpec.IsSatisfiedBy(product2) {
		t.Error("expected product2 to satisfy out-of-stock spec")
	}
}

func TestCompositeSpecification(t *testing.T) {
	inStockSpec := specification.NewSpecification(func(p Product) bool {
		return p.InStock
	})

	highRatedSpec := specification.NewSpecification(func(p Product) bool {
		return p.Rating >= 4.0
	})

	electronicsSpec := specification.NewSpecification(func(p Product) bool {
		return p.Category == "Electronics"
	})

	// All conditions must be met
	premiumSpec := specification.NewComposite[Product]("premium", "all").
		Add(inStockSpec).
		Add(highRatedSpec).
		Add(electronicsSpec)

	product1 := Product{InStock: true, Rating: 4.5, Category: "Electronics"}
	product2 := Product{InStock: true, Rating: 3.0, Category: "Electronics"}
	product3 := Product{InStock: false, Rating: 4.5, Category: "Electronics"}

	if !premiumSpec.IsSatisfiedBy(product1) {
		t.Error("expected product1 to satisfy premium spec")
	}

	if premiumSpec.IsSatisfiedBy(product2) {
		t.Error("expected product2 to not satisfy (low rating)")
	}

	if premiumSpec.IsSatisfiedBy(product3) {
		t.Error("expected product3 to not satisfy (out of stock)")
	}
}

func TestCompositeSpecification_Any(t *testing.T) {
	cheapSpec := specification.NewSpecification(func(p Product) bool {
		return p.Price <= 20.0
	})

	highRatedSpec := specification.NewSpecification(func(p Product) bool {
		return p.Rating >= 4.5
	})

	// Any condition can be met
	dealSpec := specification.NewComposite[Product]("deal", "any").
		Add(cheapSpec).
		Add(highRatedSpec)

	product1 := Product{Price: 10.0, Rating: 3.0}  // cheap
	product2 := Product{Price: 100.0, Rating: 5.0} // high rated
	product3 := Product{Price: 100.0, Rating: 3.0} // neither

	if !dealSpec.IsSatisfiedBy(product1) {
		t.Error("expected product1 to satisfy (cheap)")
	}

	if !dealSpec.IsSatisfiedBy(product2) {
		t.Error("expected product2 to satisfy (high rated)")
	}

	if dealSpec.IsSatisfiedBy(product3) {
		t.Error("expected product3 to not satisfy")
	}
}
