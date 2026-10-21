// Package results defines the home page's own read model: the DTOs the home
// views render. The home team owns this shape; the product platform's schemas
// never leak into home templates — the remote adapter maps the wire format into
// these structs at the boundary.
package results

// Product is the home page's view of a catalog product. It carries only the
// fields the storefront grid actually renders; anything beyond that stays behind
// the remote port instead of creeping into this page's read model.
//
// Barcode is always empty today: no current product contract carries one and no
// view renders it. The field is kept so a barcode-bearing contract can populate
// it without reshaping this read model — until then the zero value flows
// through unread.
type Product struct {
	ID            string
	ProductTypeID string
	SchemaVersion int32
	Attributes    map[string]string
	TenantID      string
	Name          string
	Slug          string
	Description   string
	Brand         string
	Price         int64
	Currency      string
	Stock         int
	Barcode       string

	// PrimaryMediaID is the media-service id of the product's primary image. It is
	// the product's own reference, carried here so a view can fall back to a
	// placeholder when the image has not been resolved.
	PrimaryMediaID string
	// ImageURL is the presigned URL media-service returned for PrimaryMediaID, or
	// "" when the product has no primary image or the media upstream could not be
	// reached. It is the storefront's only use of the object store — the bytes are
	// never proxied through this process.
	ImageURL string
}

// Listing is one page of products plus the paging metadata the storefront needs
// to render controls and crawl further pages.
//
// Total is the number of matching products before paging. NextCursor is always
// empty today: the browse read model pages by offset and returns no keyset
// cursor, and the storefront pages by number — pagekit's Fetch contract declares
// a cursor slot, but the handler ignores it and the UI is built from Total. The
// field is kept for a cursor-bearing read model.
type Listing struct {
	Products   []*Product
	Total      int
	NextCursor string
}
