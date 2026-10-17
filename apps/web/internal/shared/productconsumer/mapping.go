package productconsumer

import (
	"strconv"
	"strings"
	"time"

	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
)

// This file is every wire↔Core conversion in the kit, in one place, so a field
// added to a contract is added to exactly one mapper and the four call sites
// above cannot drift from each other.
//
// The three product contracts each declare their own identically-valued
// ProductStatus enum, which protobuf treats as three unrelated types. Rather than
// write three near-identical switches, each mapper normalizes through a
// per-contract helper here — so a new status value has exactly three obvious edit
// points and the neutral Status never leaks a wire type.

// Input is a create request in gogo's own shapes. Zero values are meaningful:
// an empty Currency takes the write side's default, an empty Slug is generated
// from the name, and Stock starts the product at a quantity.
type Input struct {
	ProductTypeID string
	SchemaVersion int32
	Attributes    map[string]string
	Name          string
	Slug          string
	Description   string
	Brand         string
	Price         int64 // minor units
	Currency      string
	Stock         int
	Status        Status
	CategoryID    string
	MediaID       string
	// MerchantID scopes the product to a merchant owned by the calling tenant.
	MerchantID string
	Variants   []Variant
}

// Patch is a partial update. Every field is a pointer: a nil field is left alone by
// the write side, and a pointer to a zero value is a real "set this to empty".
// That distinction is why this is not Input — an Input zero value cannot express
// "clear the description".
type Patch struct {
	ProductTypeID *string
	SchemaVersion *int32
	Attributes    map[string]string
	Name          *string
	Slug          *string
	Description   *string
	Brand         *string
	Price         *int64
	Currency      *string
	Stock         *int32
	Status        *Status
	CategoryID    *string
	MediaID       *int64
}

// categoryIDPtr converts an optional category id for the write side. An empty
// string means "unassigned" and is sent as 0, which is what the contract documents
// for an unset foreign key.
func (p Patch) categoryIDPtr() *int64 {
	if p.CategoryID == nil {
		return nil
	}
	id := numericID(*p.CategoryID)
	return &id
}

// mediaIDPtr converts an optional media id. Same 0-means-unset convention.
func (p Patch) mediaIDPtr() *int64 { return p.MediaID }

// statusPtr converts an optional status into the write side's enum pointer.
func (p Patch) statusPtr() *productv1.ProductStatus {
	if p.Status == nil {
		return nil
	}
	s := statusFromWrite(*p.Status)
	return &s
}

// numericCategoryID converts a gogo category id to the write side's int64 key.
func (i Input) numericCategoryID() int64 { return numericID(i.CategoryID) }

// numericMediaID converts a gogo media id to the write side's int64 key.
func (i Input) numericMediaID() int64 { return numericID(i.MediaID) }

// wireStatus converts a neutral status into the write side's enum.
func (i Input) wireStatus() productv1.ProductStatus { return statusFromWrite(i.Status) }

// wireVariants converts variants for the write side. A variant with no SKU is
// rejected by the write side, so an empty SKU is left as-is rather than invented
// here: the caller owns that rule, and a silent placeholder would hide it.
func (i Input) wireVariants() []*productv1.VariantSpec {
	if len(i.Variants) == 0 {
		return nil
	}
	out := make([]*productv1.VariantSpec, 0, len(i.Variants))
	for _, v := range i.Variants {
		out = append(out, &productv1.VariantSpec{
			Sku:        v.SKU,
			Name:       v.Name,
			PriceCents: v.Price,
			Currency:   v.Currency,
			Stock:      int32(v.Stock),
			MediaId:    numericID(v.MediaID),
		})
	}
	return out
}

// paging applies the default and cap to a query's limit/offset. Both read models
// cap server-side; this default exists so a caller that leaves Limit at zero gets
// a real page instead of an empty one.
func (q Query) paging() (limit, offset int) {
	limit = q.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	offset = q.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// numericCategoryID converts a gogo category id to the read contracts' int64 key.
// An unparseable id becomes 0 (unfiltered), which is the safe direction: a
// malformed filter must not become a filter that excludes everything or, worse,
// one a caller believes was applied.
func (q Query) numericCategoryID() int64 { return numericID(q.CategoryID) }

// wireStatus converts a neutral status to the browse contract's enum.
func (q Query) wireStatus() productlistingv1.ProductStatus { return statusFromBrowse(q.Status) }

// searchWireStatus converts a neutral status to the search contract's enum.
func (q Query) searchWireStatus() productsearchv1.ProductStatus { return statusFromSearch(q.Status) }

const (
	// defaultPageSize is the page a caller gets when it asks for none.
	defaultPageSize = 20
	// maxPageSize caps a caller's page. Both read models clamp server-side too;
	// this keeps an oversized request from crossing the wire at all.
	maxPageSize = 100
	// minSearchTermLen is the shortest term product-search-service will accept: a
	// trigram index cannot accelerate a shorter pattern, so the contract rejects it
	// rather than fall back to a sequential scan of the whole index.
	minSearchTermLen = 3
)

// numericID converts gogo's decimal-string id to an int64 key. A non-numeric id
// yields 0, which every contract reads as "unset" — so a malformed id degrades to a
// missing filter rather than to a wrong row.
func numericID(id string) int64 {
	if id == "" {
		return 0
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// formatID converts an int64 key to gogo's decimal-string id.
func formatID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// formatOptionalID is formatID for a field whose 0 means "unset" (parent_id).
func formatOptionalID(id int64) string { return formatID(id) }

// trimSpace normalizes a search term.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// timestamp converts an optional protobuf Timestamp to a time.Time. A nil
// timestamp is the zero time, not the epoch: it means "the contract carried no
// timestamp", which is a different fact from "this happened at 1970-01-01", and a
// caller rendering a timestamp must be able to tell them apart.
func timestamp(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// ---- status normalization --------------------------------------------------

func statusFromWrite(s Status) productv1.ProductStatus {
	switch s {
	case StatusDraft:
		return productv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case StatusActive:
		return productv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	case StatusArchived:
		return productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return productv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func statusToWrite(s productv1.ProductStatus) Status {
	switch s {
	case productv1.ProductStatus_PRODUCT_STATUS_DRAFT:
		return StatusDraft
	case productv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return StatusActive
	case productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return StatusArchived
	default:
		return StatusUnspecified
	}
}

func statusFromBrowse(s Status) productlistingv1.ProductStatus {
	switch s {
	case StatusDraft:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case StatusActive:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	case StatusArchived:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func statusToBrowse(s productlistingv1.ProductStatus) Status {
	switch s {
	case productlistingv1.ProductStatus_PRODUCT_STATUS_DRAFT:
		return StatusDraft
	case productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return StatusActive
	case productlistingv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return StatusArchived
	default:
		return StatusUnspecified
	}
}

func statusFromSearch(s Status) productsearchv1.ProductStatus {
	switch s {
	case StatusDraft:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case StatusActive:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	case StatusArchived:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func statusToSearch(s productsearchv1.ProductStatus) Status {
	switch s {
	case productsearchv1.ProductStatus_PRODUCT_STATUS_DRAFT:
		return StatusDraft
	case productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return StatusActive
	case productsearchv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return StatusArchived
	default:
		return StatusUnspecified
	}
}

// ---- wire → Core -----------------------------------------------------------

// coreFromBrowse maps the browse read model into Core.
func coreFromBrowse(p *productlistingv1.Product) Core {
	if p == nil {
		return Core{}
	}
	core := Core{
		ID:            formatID(p.GetId()),
		ProductTypeID: p.GetProductTypeId(),
		SchemaVersion: p.GetSchemaVersion(),
		Attributes:    p.GetAttributes(),
		Name:          p.GetName(),
		Slug:          p.GetSlug(),
		Description:   p.GetDescription(),
		Brand:         p.GetBrand(),
		Price:         p.GetPriceCents(),
		Currency:      p.GetCurrency(),
		Stock:         int(p.GetStock()),
		Status:        statusToBrowse(p.GetStatus()),
		CategoryID:    formatID(p.GetCategoryId()),
		MediaID:       formatID(p.GetPrimaryMediaId()),
		MerchantID:    p.GetMerchantId(),
		CreatedAt:     timestamp(p.GetCreatedAt()),
		UpdatedAt:     timestamp(p.GetUpdatedAt()),
	}
	if wire := p.GetVariants(); len(wire) > 0 {
		core.Variants = make([]Variant, 0, len(wire))
		for _, v := range wire {
			core.Variants = append(core.Variants, variantFromBrowse(v))
		}
	}
	return core
}

// coreFromWrite maps a write-side product into Core.
func coreFromWrite(p *productv1.Product) Core {
	if p == nil {
		return Core{}
	}
	core := Core{
		ID:            formatID(p.GetId()),
		ProductTypeID: p.GetProductTypeId(),
		SchemaVersion: p.GetSchemaVersion(),
		Attributes:    p.GetAttributes(),
		Name:          p.GetName(),
		Slug:          p.GetSlug(),
		Brand:         p.GetBrand(),
		Price:         p.GetPriceCents(),
		Currency:      p.GetCurrency(),
		Stock:         int(p.GetStock()),
		Status:        statusToWrite(p.GetStatus()),
		CategoryID:    formatID(p.GetCategoryId()),
		MediaID:       formatID(p.GetPrimaryMediaId()),
		MerchantID:    p.GetMerchantId(),
		CreatedAt:     timestamp(p.GetCreatedAt()),
		UpdatedAt:     timestamp(p.GetUpdatedAt()),
	}
	if wire := p.GetVariants(); len(wire) > 0 {
		core.Variants = make([]Variant, 0, len(wire))
		for _, v := range wire {
			core.Variants = append(core.Variants, Variant{
				ID:        formatID(v.GetId()),
				ProductID: formatID(v.GetProductId()),
				SKU:       v.GetSku(),
				Name:      v.GetName(),
				Price:     v.GetPriceCents(),
				Currency:  v.GetCurrency(),
				Stock:     int(v.GetStock()),
				MediaID:   formatID(v.GetMediaId()),
			})
		}
	}
	return core
}

// coreFromSearch maps a search document into Core. A search document is
// deliberately flatter than a full product: a page of results must not grow with
// the number of variants per product, so Variants is always empty here even
// though a search hit carries the same id as a browsable product.
func coreFromSearch(p *productsearchv1.SearchProduct) Core {
	if p == nil {
		return Core{}
	}
	return Core{
		ID:            formatID(p.GetId()),
		ProductTypeID: p.GetProductTypeId(),
		SchemaVersion: p.GetSchemaVersion(),
		Attributes:    p.GetAttributes(),
		Name:          p.GetName(),
		Slug:          p.GetSlug(),
		Description:   p.GetDescription(),
		Brand:         p.GetBrand(),
		Price:         p.GetPriceCents(),
		Currency:      p.GetCurrency(),
		Stock:         int(p.GetStock()),
		Status:        statusToSearch(p.GetStatus()),
		CategoryID:    formatID(p.GetCategoryId()),
		MediaID:       formatID(p.GetPrimaryMediaId()),
		MerchantID:    p.GetMerchantId(),
		CreatedAt:     timestamp(p.GetCreatedAt()),
		UpdatedAt:     timestamp(p.GetUpdatedAt()),
	}
}

func variantFromBrowse(v *productlistingv1.Variant) Variant {
	if v == nil {
		return Variant{}
	}
	return Variant{
		ID:        formatID(v.GetId()),
		ProductID: formatID(v.GetProductId()),
		SKU:       v.GetSku(),
		Name:      v.GetName(),
		Price:     v.GetPriceCents(),
		Currency:  v.GetCurrency(),
		Stock:     int(v.GetStock()),
		MediaID:   formatID(v.GetMediaId()),
	}
}
