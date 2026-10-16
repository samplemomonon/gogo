// Package model holds the types the read API serves.
//
// These mirror product-service's domain types, but they are separate
// declarations. The read model is a projection, and it is entitled to be shaped
// for serving reads rather than for storing writes: a future listing view may
// carry a computed field or a denormalised category name that the write side has
// no reason to know about. Sharing the types would make every read-side addition
// a change to the write service.
package model

import (
	"fmt"
	"strings"
	"time"
)

// Status is the publication state of a product. Only active products are
// expected to be visible to shoppers.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// ParseStatus converts a client supplied string to a Status, rejecting unknown
// values so a typo cannot silently return everything.
func ParseStatus(s string) (Status, error) {
	switch st := Status(strings.ToLower(strings.TrimSpace(s))); st {
	case StatusDraft, StatusActive, StatusArchived:
		return st, nil
	case "":
		return StatusDraft, nil
	default:
		return "", fmt.Errorf("unknown product status %q, want draft|active|archived", s)
	}
}

// Product is a sellable item as the read model holds it.
//
// Money is minor units in an int64 rather than a float, matching the write side:
// binary floating point cannot represent decimal currency exactly, and a listing
// that reinterprets the price would show a shopper a number the writer never
// meant. CategoryID and PrimaryMediaID reference other services and so have no
// foreign key here either.
type Product struct {
	ID             int64             `json:"id"`
	ProductTypeID  string            `json:"product_type_id"`
	SchemaVersion  int32             `json:"schema_version"`
	Attributes     map[string]string `json:"attributes"`
	CategoryID     int64             `json:"category_id"`
	Name           string            `json:"name"`
	Slug           string            `json:"slug"`
	Description    *string           `json:"description"`
	Brand          *string           `json:"brand"`
	PriceCents     int64             `json:"price_cents"`
	Currency       string            `json:"currency"`
	Stock          int               `json:"stock"`
	Status         Status            `json:"status"`
	PrimaryMediaID *int64            `json:"primary_media_id"`
	Variants       []*Variant        `json:"variants"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	// ProjectedAt is when the read model last wrote this row, which is not the
	// same as UpdatedAt: an event can republish a product without changing it.
	// It is exposed so a caller can tell how current its data is.
	ProjectedAt time.Time `json:"projected_at"`
}

// Variant is a purchasable size, colour or bundle of a product.
type Variant struct {
	ID         int64     `json:"id"`
	ProductID  int64     `json:"product_id"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	Currency   string    `json:"currency"`
	Stock      int       `json:"stock"`
	MediaID    *int64    `json:"media_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// ProjectedAt is when the read model last wrote this row, which is not the
	// same as UpdatedAt: an event can republish a variant without changing it.
	ProjectedAt time.Time `json:"projected_at"`
}

// ListProductsQuery are the filters accepted by GET /api/v1/products.
//
// There is deliberately no free-text term here. Browsing and searching answer
// different questions and have different costs, so they live in different
// services: browsing is a bounded indexed scan, while searching needs trigram
// indexes, relevance ranking, and eventually a separate engine. product-search-service
// owns that, and it projects the same events into its own read model.
type ListProductsQuery struct {
	// CategoryID filters to one category.
	CategoryID int64
	// Status is a pointer so "no filter" is distinct from "draft".
	Status *Status
	// MinPriceCents and MaxPriceCents bound the price, and are pointers so that
	// "no filter" is distinct from "zero".
	//
	// A plain int64 cannot express both. Zero means unset is convenient right up
	// until someone asks for a zero-price filter, and then it silently returns the
	// unfiltered catalogue instead of the empty set that was asked for — a caller
	// gets more rows than they requested and no indication anything went wrong.
	// A pointer is the same shape Status uses above, for the same reason.
	MinPriceCents   *int64
	MaxPriceCents   *int64
	Limit           int
	Offset          int
	IncludeVariants bool
}

// ListProductsResult is a page of products together with the unpaged total.
type ListProductsResult struct {
	Products []*Product
	Total    int64
}

// Slugify converts a name into a URL slug. The read model never derives a slug
// from a name, but it is kept here so the helper is available to tests and to
// any future view that has to build a URL, and so both services agree on what a
// slug looks like.
func Slugify(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	lastWasHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasHyphen = false
		default:
			// Collapse every run of separators into one hyphen.
			if !lastWasHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastWasHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
