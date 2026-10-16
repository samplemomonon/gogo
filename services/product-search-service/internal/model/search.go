// Package model holds the types the search API serves.
//
// These mirror the product snapshots product-service publishes, but they are a
// separate declaration and a narrower shape. A search result is a card, not a
// product: it carries what a shopper needs to pick one of several results, and
// deliberately not the variant list, because a page of twenty results would then
// grow with the number of variants per product.
//
// The duplication is the same deliberate choice product-listing-service makes.
// Sharing Go types would mean a dependency between two services that are meant to
// deploy independently, and a mismatch would become a compile error forcing a
// coordinated release instead of a decode error in one consumer.
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

// SearchProduct is one indexed product as the search read model holds it.
//
// Money is minor units in an int64, matching the write side: binary floating
// point cannot represent decimal currency exactly, and a result that reinterprets
// the price would show a shopper a number the writer never meant. CategoryID and
// PrimaryMediaID reference other services and so carry no foreign key here.
type SearchProduct struct {
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
	// Score is how well this document matched the query. It is only comparable
	// between hits of one response, and it is engine-specific: under pg_trgm it is
	// a trigram word similarity, under Elasticsearch it would be _score. Clients
	// must not compare scores across queries or across engine versions.
	Score float64 `json:"score"`
	// ProjectedAt is when this read model last wrote the row, which is not the
	// same as UpdatedAt: an event can republish a product without changing it.
	ProjectedAt time.Time `json:"projected_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SearchQuery is one search request.
//
// Every field except Query is a filter applied to the documents the term already
// matched. They are kept together rather than offered as a separate filtering
// endpoint because the index that answers the term is the only place these
// filters can be applied without a second lookup.
type SearchQuery struct {
	// Query is the free-text term. It is required, and the service enforces a
	// minimum length because a trigram index cannot accelerate a shorter pattern.
	Query string
	// CategoryID filters to one category.
	CategoryID int64
	// Status is a pointer so "no filter" is distinct from "draft".
	Status *Status
	// MinPriceCents and MaxPriceCents bound the price, and are pointers so that
	// "no filter" is distinct from "zero".
	//
	// A plain int64 cannot express both. Zero means unset is convenient right up
	// until someone asks for a zero-price filter, and then it silently returns the
	// unfiltered index instead of the empty set that was asked for. A pointer is
	// the same shape Status uses above, for the same reason.
	MinPriceCents *int64
	MaxPriceCents *int64
	Limit         int
	Offset        int
}

// SearchResult is a ranked page of documents plus the unpaged total.
//
// TookMS is the engine's own measurement of how long the query took. It is
// reported because the number of results is not the signal to watch for: a
// search that is still correct but slow is the condition that says this read
// model has outgrown PostgreSQL, and that is the moment to move to a dedicated
// engine rather than after it starts failing.
type SearchResult struct {
	Products []*SearchProduct
	Total    int64
	Limit    int
	Offset   int
	TookMS   int64
}
