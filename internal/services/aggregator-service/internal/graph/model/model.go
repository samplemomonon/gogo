// Package model holds the domain types the GraphQL resolvers exchange.
//
// These are deliberately separate from both the protobuf messages and the
// generated GraphQL models. Keeping them distinct buys three things:
//
//   - The protobuf enums use SCREAMING_SNAKE names because that is the proto
//     convention; the GraphQL enums use PascalCase because that is the GraphQL
//     convention. Mapping in one place keeps both idiomatic instead of leaking
//     one into the other.
//   - Money stays int64 all the way through. GraphQL's Int is a 32-bit signed
//     integer, so a price in minor units is serialised as Int64String. An int32
//     silently truncating a price above ~21 million minor units is exactly the
//     class of bug a composition layer should not introduce.
//   - The resolvers never see protobuf types, so swapping a downstream transport
//     does not ripple into the schema.
package model

import (
	"errors"
	"fmt"
	"time"
)

// Status mirrors product.v1.ProductStatus.
type Status string

const (
	StatusDraft    Status = "DRAFT"
	StatusActive   Status = "ACTIVE"
	StatusArchived Status = "ARCHIVED"
	StatusUnknown  Status = ""
)

// ParseStatus maps a client-supplied GraphQL enum value. An empty input means
// "not specified", which every write contract treats as "leave it alone" or "use
// the default"; distinguishing that from an explicit value is the caller's job,
// not the parser's.
func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusDraft, StatusActive, StatusArchived:
		return Status(s), nil
	case "":
		return StatusUnknown, nil
	default:
		return StatusUnknown, fmt.Errorf("unknown product status %q", s)
	}
}

// MediaKind mirrors media.v1.MediaKind.
type MediaKind string

const (
	MediaKindImage    MediaKind = "IMAGE"
	MediaKindVideo    MediaKind = "VIDEO"
	MediaKindAudio    MediaKind = "AUDIO"
	MediaKindDocument MediaKind = "DOCUMENT"
	MediaKindOther    MediaKind = "OTHER"
	MediaKindUnknown  MediaKind = ""
)

// ParseMediaKind maps a client-supplied GraphQL enum value.
func ParseMediaKind(s string) (MediaKind, error) {
	switch MediaKind(s) {
	case MediaKindImage, MediaKindVideo, MediaKindAudio, MediaKindDocument, MediaKindOther:
		return MediaKind(s), nil
	case "":
		return MediaKindUnknown, nil
	default:
		return MediaKindUnknown, fmt.Errorf("unknown media kind %q", s)
	}
}

// MediaStatus mirrors media.v1.MediaStatus.
type MediaStatus string

const (
	MediaStatusPending MediaStatus = "PENDING"
	MediaStatusReady   MediaStatus = "READY"
	MediaStatusUnknown MediaStatus = ""
)

// ParseMediaStatus maps a client-supplied GraphQL enum value.
func ParseMediaStatus(s string) (MediaStatus, error) {
	switch MediaStatus(s) {
	case MediaStatusPending, MediaStatusReady:
		return MediaStatus(s), nil
	case "":
		return MediaStatusUnknown, nil
	default:
		return MediaStatusUnknown, fmt.Errorf("unknown media status %q", s)
	}
}

// Category is a node in the hierarchy, as category-service knows it.
type Category struct {
	ID          string
	Name        string
	Description string
	ParentID    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Variant is a purchasable size or colour.
type Variant struct {
	ID         string
	ProductID  string
	SKU        string
	Name       string
	PriceCents int64
	Currency   string
	Stock      int32
	MediaID    string
}

// Product is a product as the listing read model holds it.
type Product struct {
	ID             string
	CategoryID     string
	Name           string
	Slug           string
	Description    string
	Brand          string
	PriceCents     int64
	Currency       string
	Stock          int32
	Status         Status
	PrimaryMediaID string
	Variants       []Variant
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// ProjectedAt is set only by the read models. A product returned by a write
	// has no projection yet, which is how the two are told apart.
	ProjectedAt *time.Time
	// HasVariants distinguishes "the read model returned no variants" from "no
	// variants were requested". Without it a client cannot tell an empty product
	// from a truncated one.
	HasVariants bool
}

// SearchHit is one search result.
type SearchHit struct {
	Product
	Score float64
}

// Media is a stored object's metadata.
//
// DownloadURL is carried on the record rather than resolved per instance. The reason is
// structural, not stylistic: gqlgen only coalesces a batch resolver when it marshals a
// real list of parents, and a product's primaryMedia is one object per product, not a
// list. A resolver on Media.downloadUrl therefore runs once per media object, which for
// a page of twenty products is twenty calls to media-service where one would do. Stamping
// the URL in the batch that already fetched the metadata makes the cost one call per
// page, which is the property the whole service is built around.
//
// It is a pointer so "no URL" and "empty URL" stay distinguishable, and so an object
// that has not been through a signing batch is visibly unsigning rather than presenting
// an empty string as a valid URL.
type Media struct {
	ID          string
	OwnerID     string
	Filename    string
	ContentType string
	SizeBytes   int64
	ETag        string
	Kind        MediaKind
	Status      MediaStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// DownloadURL is set by whatever resolver produced this record, in the same batch
	// that fetched it. Nil means no URL was signed.
	DownloadURL *string
}

// UploadTarget is a presigned PUT destination plus the headers it requires.
type UploadTarget struct {
	URL       string
	Method    string
	Headers   []Header
	ExpiresAt time.Time
}

// Header is one required request header.
type Header struct {
	Name  string
	Value string
}

// MediaUpload is a registered record plus its upload target.
type MediaUpload struct {
	Media  Media
	Upload UploadTarget
}

// ProductPage is a page of browsed products.
type ProductPage struct {
	Products []Product
	Total    int64
	Limit    int32
	Offset   int32
}

// SearchResult is a page of search hits.
type SearchResult struct {
	Hits   []SearchHit
	Total  int64
	Limit  int32
	Offset int32
	TookMS int64
}

// MediaPage is a page of media records.
type MediaPage struct {
	Media []Media
	Total int64
}

// CategoryPage is a page of categories.
type CategoryPage struct {
	Categories []Category
	Total      int64
}

// ProjectionStatus is one read model's progress.
type ProjectionStatus struct {
	Records       int64
	AppliedEvents int64
	Healthy       bool
	Source        string
}

// ProjectionHealth is both read models' progress.
type ProjectionHealth struct {
	Listing *ProjectionStatus
	Search  *ProjectionStatus
}

// VariantInput is a variant to create alongside its product.
type VariantInput struct {
	SKU        string
	Name       string
	PriceCents int64
	Currency   string
	Stock      int32
	MediaID    string
}

// CreateProductInput is a validated create request.
type CreateProductInput struct {
	CategoryID     string
	Name           string
	Slug           string
	Description    string
	Brand          string
	PriceCents     int64
	Currency       string
	Stock          int32
	Status         Status
	PrimaryMediaID string
	Variants       []VariantInput
}

// UpdateProductInput is a partial update. A nil pointer means "leave alone".
type UpdateProductInput struct {
	CategoryID     *string
	Name           *string
	Slug           *string
	Description    *string
	Brand          *string
	PriceCents     *int64
	Currency       *string
	Stock          *int32
	Status         *Status
	PrimaryMediaID *string
}

// IsEmpty reports whether the update would change nothing. A write contract that
// receives an update with no fields set answers "invalid input" rather than
// silently succeeding, so the aggregator surfaces that instead of pretending.
func (i UpdateProductInput) IsEmpty() bool {
	return i.CategoryID == nil && i.Name == nil && i.Slug == nil &&
		i.Description == nil && i.Brand == nil && i.PriceCents == nil &&
		i.Currency == nil && i.Stock == nil && i.Status == nil &&
		i.PrimaryMediaID == nil
}

// CreateCategoryInput is a validated create request.
type CreateCategoryInput struct {
	Name        string
	Description string
	ParentID    string
}

// UpdateCategoryInput is a partial update. A nil pointer means "leave alone".
type UpdateCategoryInput struct {
	Name        *string
	Description *string
	ParentID    *string
}

// RegisterMediaInput is a validated upload registration.
type RegisterMediaInput struct {
	OwnerID     string
	Filename    string
	ContentType string
	Kind        MediaKind
	SizeBytes   int64
}

// ErrNotFound is returned when an entity does not exist in its owning service.
var ErrNotFound = errors.New("not found")
