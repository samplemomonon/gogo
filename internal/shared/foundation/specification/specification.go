// Package specification implements the Specification pattern for complex business rules.
// Essential for e-commerce: product filtering, pricing rules, discount eligibility, etc.
package specification

import (
	"fmt"
)

// Specification defines a business rule that can be satisfied by an entity.
type Specification[T any] interface {
	IsSatisfiedBy(entity T) bool
	And(other Specification[T]) Specification[T]
	Or(other Specification[T]) Specification[T]
	Not() Specification[T]
}

// BaseSpecification provides default And/Or/Not implementations.
type BaseSpecification[T any] struct {
	predicate func(T) bool
}

// NewSpecification creates a new specification from a predicate function.
func NewSpecification[T any](predicate func(T) bool) Specification[T] {
	return &BaseSpecification[T]{predicate: predicate}
}

func (s *BaseSpecification[T]) IsSatisfiedBy(entity T) bool {
	return s.predicate(entity)
}

func (s *BaseSpecification[T]) And(other Specification[T]) Specification[T] {
	return &andSpecification[T]{left: s, right: other}
}

func (s *BaseSpecification[T]) Or(other Specification[T]) Specification[T] {
	return &orSpecification[T]{left: s, right: other}
}

func (s *BaseSpecification[T]) Not() Specification[T] {
	return &notSpecification[T]{spec: s}
}

// andSpecification combines two specs with AND logic.
type andSpecification[T any] struct {
	left  Specification[T]
	right Specification[T]
}

func (s *andSpecification[T]) IsSatisfiedBy(entity T) bool {
	return s.left.IsSatisfiedBy(entity) && s.right.IsSatisfiedBy(entity)
}

func (s *andSpecification[T]) And(other Specification[T]) Specification[T] {
	return &andSpecification[T]{left: s, right: other}
}

func (s *andSpecification[T]) Or(other Specification[T]) Specification[T] {
	return &orSpecification[T]{left: s, right: other}
}

func (s *andSpecification[T]) Not() Specification[T] {
	return &notSpecification[T]{spec: s}
}

// orSpecification combines two specs with OR logic.
type orSpecification[T any] struct {
	left  Specification[T]
	right Specification[T]
}

func (s *orSpecification[T]) IsSatisfiedBy(entity T) bool {
	return s.left.IsSatisfiedBy(entity) || s.right.IsSatisfiedBy(entity)
}

func (s *orSpecification[T]) And(other Specification[T]) Specification[T] {
	return &andSpecification[T]{left: s, right: other}
}

func (s *orSpecification[T]) Or(other Specification[T]) Specification[T] {
	return &orSpecification[T]{left: s, right: other}
}

func (s *orSpecification[T]) Not() Specification[T] {
	return &notSpecification[T]{spec: s}
}

// notSpecification negates a spec.
type notSpecification[T any] struct {
	spec Specification[T]
}

func (s *notSpecification[T]) IsSatisfiedBy(entity T) bool {
	return !s.spec.IsSatisfiedBy(entity)
}

func (s *notSpecification[T]) And(other Specification[T]) Specification[T] {
	return &andSpecification[T]{left: s, right: other}
}

func (s *notSpecification[T]) Or(other Specification[T]) Specification[T] {
	return &orSpecification[T]{left: s, right: other}
}

func (s *notSpecification[T]) Not() Specification[T] {
	return &notSpecification[T]{spec: s}
}

// CompositeSpecification allows building complex specs with named rules.
type CompositeSpecification[T any] struct {
	name  string
	specs []Specification[T]
	mode  string // "all" or "any"
}

// NewComposite creates a composite specification.
func NewComposite[T any](name string, mode string) *CompositeSpecification[T] {
	return &CompositeSpecification[T]{
		name:  name,
		mode:  mode,
		specs: make([]Specification[T], 0),
	}
}

// Add adds a specification to the composite.
func (c *CompositeSpecification[T]) Add(spec Specification[T]) *CompositeSpecification[T] {
	c.specs = append(c.specs, spec)
	return c
}

// IsSatisfiedBy checks if entity satisfies the composite spec.
func (c *CompositeSpecification[T]) IsSatisfiedBy(entity T) bool {
	if len(c.specs) == 0 {
		return true
	}

	if c.mode == "all" {
		for _, spec := range c.specs {
			if !spec.IsSatisfiedBy(entity) {
				return false
			}
		}
		return true
	}

	// "any" mode
	for _, spec := range c.specs {
		if spec.IsSatisfiedBy(entity) {
			return true
		}
	}
	return false
}

func (c *CompositeSpecification[T]) And(other Specification[T]) Specification[T] {
	return &andSpecification[T]{left: c, right: other}
}

func (c *CompositeSpecification[T]) Or(other Specification[T]) Specification[T] {
	return &orSpecification[T]{left: c, right: other}
}

func (c *CompositeSpecification[T]) Not() Specification[T] {
	return &notSpecification[T]{spec: c}
}

// String returns a readable description.
func (c *CompositeSpecification[T]) String() string {
	return fmt.Sprintf("%s (%s of %d rules)", c.name, c.mode, len(c.specs))
}

// Example usage for e-commerce:
//
// // Product specifications
// type Product struct {
//     Price     float64
//     Category  string
//     InStock   bool
//     Rating    float64
// }
//
// inStockSpec := specification.NewSpecification(func(p Product) bool {
//     return p.InStock
// })
//
// affordableSpec := specification.NewSpecification(func(p Product) bool {
//     return p.Price <= 100.0
// })
//
// highRatedSpec := specification.NewSpecification(func(p Product) bool {
//     return p.Rating >= 4.0
// })
//
// // Combine specifications
// eligibleForDiscount := inStockSpec.And(affordableSpec).And(highRatedSpec)
//
// if eligibleForDiscount.IsSatisfiedBy(product) {
//     // Apply discount
// }
//
// // Or use composite for complex rules
// premiumProduct := specification.NewComposite[Product]("premium", "all").
//     Add(highRatedSpec).
//     Add(inStockSpec).
//     Add(specification.NewSpecification(func(p Product) bool {
//         return p.Category == "Electronics"
//     }))
