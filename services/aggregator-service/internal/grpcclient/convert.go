package grpcclient

import (
	"fmt"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"aggregator-service/gen/categorypb"
	"aggregator-service/gen/listingpb"
	"aggregator-service/gen/mediapb"
	"aggregator-service/gen/productpb"
	"aggregator-service/gen/searchpb"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// unavailableSentinel is the retryable error, aliased so the conversion helpers
// in this file do not have to import apperror for a single wrap.
var unavailableSentinel = apperror.ErrUnavailable

// Header is one required upload header.
type Header = model.Header

// MediaUpload is a registered record plus its upload target.
type MediaUpload = model.MediaUpload

// uploadTargetFromProto builds an upload target with a deterministic header order.
func uploadTargetFromProto(upload *mediapb.UploadURL, headers []Header) model.UploadTarget {
	return model.UploadTarget{
		URL:       upload.GetUrl(),
		Method:    upload.GetMethod(),
		Headers:   headers,
		ExpiresAt: timeFromProto(upload.GetExpiresAt()),
	}
}

// parseID converts a GraphQL ID to the int64 every downstream contract uses.
//
// A non-numeric id is a client error rather than a downstream fault, so it is
// classified as invalid input and reported without a round trip. Silently
// coercing it to 0 would either fetch the wrong entity or return a confusing
// not-found for a record that was never addressed.
func parseID(id string) (int64, error) {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: id %q is not a valid identifier", apperror.ErrInvalidInput, id)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%w: id %q must be positive", apperror.ErrInvalidInput, id)
	}
	return v, nil
}

// parseOptionalID maps an absent reference to 0, which every contract documents
// as "unset". An empty string is the GraphQL way of saying the same thing.
func parseOptionalID(id string) (int64, error) {
	if id == "" {
		return 0, nil
	}
	return parseID(id)
}

func idFromInt64(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// timeFromProto converts a protobuf timestamp. A nil or zero timestamp becomes
// the zero time, which renders as null in GraphQL rather than as year 1.
func timeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil || !ts.IsValid() {
		return time.Time{}
	}
	return ts.AsTime()
}

// timePtrFromProto reports the zero time as absent, so a read model that has not
// projected a row yet yields null instead of a misleading epoch value.
func timePtrFromProto(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil || !ts.IsValid() {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// --- status ----------------------------------------------------------------

// statusToProto maps a domain status to the write contract's enum. An unspecified
// status maps to the zero value, which the service reads as "use the default".
//
// One mapper per contract on purpose. The three services declare structurally
// identical enums but version them independently, so sharing one mapper would
// couple two contracts that are free to diverge. If the listing service ever
// renumbered its enum, a shared mapper would silently misreport status through the
// write path too.
func statusToProto(s model.Status) (productpb.ProductStatus, error) {
	switch s {
	case model.StatusUnknown:
		return productpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED, nil
	case model.StatusDraft:
		return productpb.ProductStatus_PRODUCT_STATUS_DRAFT, nil
	case model.StatusActive:
		return productpb.ProductStatus_PRODUCT_STATUS_ACTIVE, nil
	case model.StatusArchived:
		return productpb.ProductStatus_PRODUCT_STATUS_ARCHIVED, nil
	default:
		return productpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED,
			fmt.Errorf("%w: unknown product status %q", apperror.ErrInvalidInput, s)
	}
}

func listingStatusToProto(s model.Status) (listingpb.ProductStatus, error) {
	switch s {
	case model.StatusUnknown:
		return listingpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED, nil
	case model.StatusDraft:
		return listingpb.ProductStatus_PRODUCT_STATUS_DRAFT, nil
	case model.StatusActive:
		return listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE, nil
	case model.StatusArchived:
		return listingpb.ProductStatus_PRODUCT_STATUS_ARCHIVED, nil
	default:
		return listingpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED,
			fmt.Errorf("%w: unknown product status %q", apperror.ErrInvalidInput, s)
	}
}

func searchStatusToProto(s model.Status) (searchpb.ProductStatus, error) {
	switch s {
	case model.StatusUnknown:
		return searchpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED, nil
	case model.StatusDraft:
		return searchpb.ProductStatus_PRODUCT_STATUS_DRAFT, nil
	case model.StatusActive:
		return searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE, nil
	case model.StatusArchived:
		return searchpb.ProductStatus_PRODUCT_STATUS_ARCHIVED, nil
	default:
		return searchpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED,
			fmt.Errorf("%w: unknown product status %q", apperror.ErrInvalidInput, s)
	}
}

// statusFromProto maps the protobuf enum to the domain status. The three services
// that declare this enum each spell it identically; an unspecified value maps to
// the empty status, which the GraphQL layer renders as null rather than
// inventing a default the service never reported.
func statusFromProto(s productpb.ProductStatus) model.Status {
	switch s {
	case productpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return model.StatusDraft
	case productpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return model.StatusActive
	case productpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return model.StatusArchived
	default:
		return model.StatusUnknown
	}
}

// listingStatusFromProto and searchStatusFromProto exist because the listing and
// search contracts each declare their own enum with the same numbering. Mapping
// through one shared function would silently couple two independently versioned
// contracts; if one service ever renumbered its enum, the other would be
// misreported through a shared decoder.
func listingStatusFromProto(s listingpb.ProductStatus) model.Status {
	switch s {
	case listingpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return model.StatusDraft
	case listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return model.StatusActive
	case listingpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return model.StatusArchived
	default:
		return model.StatusUnknown
	}
}

func searchStatusFromProto(s searchpb.ProductStatus) model.Status {
	switch s {
	case searchpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return model.StatusDraft
	case searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return model.StatusActive
	case searchpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return model.StatusArchived
	default:
		return model.StatusUnknown
	}
}

// --- media enums -----------------------------------------------------------

func mediaKindToProto(k model.MediaKind) (mediapb.MediaKind, error) {
	switch k {
	case model.MediaKindUnknown:
		return mediapb.MediaKind_MEDIA_KIND_UNSPECIFIED, nil
	case model.MediaKindImage:
		return mediapb.MediaKind_MEDIA_KIND_IMAGE, nil
	case model.MediaKindVideo:
		return mediapb.MediaKind_MEDIA_KIND_VIDEO, nil
	case model.MediaKindAudio:
		return mediapb.MediaKind_MEDIA_KIND_AUDIO, nil
	case model.MediaKindDocument:
		return mediapb.MediaKind_MEDIA_KIND_DOCUMENT, nil
	case model.MediaKindOther:
		return mediapb.MediaKind_MEDIA_KIND_OTHER, nil
	default:
		return mediapb.MediaKind_MEDIA_KIND_UNSPECIFIED,
			fmt.Errorf("%w: unknown media kind %q", apperror.ErrInvalidInput, k)
	}
}

func mediaKindFromProto(k mediapb.MediaKind) model.MediaKind {
	switch k {
	case mediapb.MediaKind_MEDIA_KIND_IMAGE:
		return model.MediaKindImage
	case mediapb.MediaKind_MEDIA_KIND_VIDEO:
		return model.MediaKindVideo
	case mediapb.MediaKind_MEDIA_KIND_AUDIO:
		return model.MediaKindAudio
	case mediapb.MediaKind_MEDIA_KIND_DOCUMENT:
		return model.MediaKindDocument
	case mediapb.MediaKind_MEDIA_KIND_OTHER:
		return model.MediaKindOther
	default:
		return model.MediaKindUnknown
	}
}

func mediaStatusToProto(s model.MediaStatus) (mediapb.MediaStatus, error) {
	switch s {
	case model.MediaStatusUnknown:
		return mediapb.MediaStatus_MEDIA_STATUS_UNSPECIFIED, nil
	case model.MediaStatusPending:
		return mediapb.MediaStatus_MEDIA_STATUS_PENDING, nil
	case model.MediaStatusReady:
		return mediapb.MediaStatus_MEDIA_STATUS_READY, nil
	default:
		return mediapb.MediaStatus_MEDIA_STATUS_UNSPECIFIED,
			fmt.Errorf("%w: unknown media status %q", apperror.ErrInvalidInput, s)
	}
}

func mediaStatusFromProto(s mediapb.MediaStatus) model.MediaStatus {
	switch s {
	case mediapb.MediaStatus_MEDIA_STATUS_PENDING:
		return model.MediaStatusPending
	case mediapb.MediaStatus_MEDIA_STATUS_READY:
		return model.MediaStatusReady
	default:
		return model.MediaStatusUnknown
	}
}

// --- entity conversions ----------------------------------------------------

func categoryFromProto(c *categorypb.Category) model.Category {
	return model.Category{
		ID:          idFromInt64(c.GetId()),
		Name:        c.GetName(),
		Description: c.GetDescription(),
		ParentID:    idFromInt64(c.GetParentId()),
		CreatedAt:   timeFromProto(c.GetCreatedAt()),
		UpdatedAt:   timeFromProto(c.GetUpdatedAt()),
	}
}

func variantFromProto(v *listingpb.Variant) model.Variant {
	return model.Variant{
		ID:         idFromInt64(v.GetId()),
		ProductID:  idFromInt64(v.GetProductId()),
		SKU:        v.GetSku(),
		Name:       v.GetName(),
		PriceCents: v.GetPriceCents(),
		Currency:   v.GetCurrency(),
		Stock:      v.GetStock(),
		MediaID:    idFromInt64(v.GetMediaId()),
	}
}

func productFromProto(p *productpb.Product) model.Product {
	variants := make([]model.Variant, 0, len(p.GetVariants()))
	for _, v := range p.GetVariants() {
		variants = append(variants, model.Variant{
			ID:         idFromInt64(v.GetId()),
			ProductID:  idFromInt64(v.GetProductId()),
			SKU:        v.GetSku(),
			Name:       v.GetName(),
			PriceCents: v.GetPriceCents(),
			Currency:   v.GetCurrency(),
			Stock:      v.GetStock(),
			MediaID:    idFromInt64(v.GetMediaId()),
		})
	}
	return model.Product{
		ID:             idFromInt64(p.GetId()),
		CategoryID:     idFromInt64(p.GetCategoryId()),
		Name:           p.GetName(),
		Slug:           p.GetSlug(),
		Description:    p.GetDescription(),
		Brand:          p.GetBrand(),
		PriceCents:     p.GetPriceCents(),
		Currency:       p.GetCurrency(),
		Stock:          p.GetStock(),
		Status:         statusFromProto(p.GetStatus()),
		PrimaryMediaID: idFromInt64(p.GetPrimaryMediaId()),
		Variants:       variants,
		CreatedAt:      timeFromProto(p.GetCreatedAt()),
		UpdatedAt:      timeFromProto(p.GetUpdatedAt()),
		// The write contract has no projected_at field, so it stays nil. That is
		// what tells a client this product has not been read back from a
		// projection yet.
		ProjectedAt: nil,
		HasVariants: len(variants) > 0,
	}
}

func listingProductFromProto(p *listingpb.Product) model.Product {
	variants := make([]model.Variant, 0, len(p.GetVariants()))
	for _, v := range p.GetVariants() {
		variants = append(variants, variantFromProto(v))
	}
	return model.Product{
		ID:             idFromInt64(p.GetId()),
		CategoryID:     idFromInt64(p.GetCategoryId()),
		Name:           p.GetName(),
		Slug:           p.GetSlug(),
		Description:    p.GetDescription(),
		Brand:          p.GetBrand(),
		PriceCents:     p.GetPriceCents(),
		Currency:       p.GetCurrency(),
		Stock:          p.GetStock(),
		Status:         listingStatusFromProto(p.GetStatus()),
		PrimaryMediaID: idFromInt64(p.GetPrimaryMediaId()),
		Variants:       variants,
		CreatedAt:      timeFromProto(p.GetCreatedAt()),
		UpdatedAt:      timeFromProto(p.GetUpdatedAt()),
		ProjectedAt:    timePtrFromProto(p.GetProjectedAt()),
		HasVariants:    true,
	}
}

func searchProductFromProto(p *searchpb.SearchProduct) model.Product {
	return model.Product{
		ID:             idFromInt64(p.GetId()),
		CategoryID:     idFromInt64(p.GetCategoryId()),
		Name:           p.GetName(),
		Slug:           p.GetSlug(),
		Description:    p.GetDescription(),
		Brand:          p.GetBrand(),
		PriceCents:     p.GetPriceCents(),
		Currency:       p.GetCurrency(),
		Stock:          p.GetStock(),
		Status:         searchStatusFromProto(p.GetStatus()),
		PrimaryMediaID: idFromInt64(p.GetPrimaryMediaId()),
		// A search document carries no variants, so HasVariants is false and the
		// field resolves to an empty list rather than a truncated one.
		HasVariants: false,
		CreatedAt:   timeFromProto(p.GetCreatedAt()),
		UpdatedAt:   timeFromProto(p.GetUpdatedAt()),
		ProjectedAt: timePtrFromProto(p.GetProjectedAt()),
	}
}

func mediaFromProto(m *mediapb.Media) model.Media {
	return model.Media{
		ID:          idFromInt64(m.GetId()),
		OwnerID:     m.GetOwnerId(),
		Filename:    m.GetFilename(),
		ContentType: m.GetContentType(),
		SizeBytes:   m.GetSizeBytes(),
		ETag:        m.GetEtag(),
		Kind:        mediaKindFromProto(m.GetKind()),
		Status:      mediaStatusFromProto(m.GetStatus()),
		CreatedAt:   timeFromProto(m.GetCreatedAt()),
		UpdatedAt:   timeFromProto(m.GetUpdatedAt()),
	}
}

// --- tree helpers ----------------------------------------------------------

// flattenTree turns the nested CategoryNode response into a flat slice, preserving
// the order the service returned. Ancestors are resolved from parent_id rather
// than from the nesting, so a client that walks the flat list still sees the same
// hierarchy.
func flattenTree(roots []*categorypb.CategoryNode) []model.Category {
	out := make([]model.Category, 0, len(roots))
	var walk func(nodes []*categorypb.CategoryNode)
	walk = func(nodes []*categorypb.CategoryNode) {
		for _, node := range nodes {
			if cat := node.GetCategory(); cat != nil {
				out = append(out, categoryFromProto(cat))
			}
			walk(node.GetChildren())
		}
	}
	walk(roots)
	return out
}

// countDescendants counts every node strictly below target, excluding target
// itself.
//
// It answers the question the deletion-impact endpoint exists for: how many
// categories would be affected by deleting this one. Counting the target would
// inflate that by one and make the number disagree with the tree it describes.
//
// The walk guards against cycles. A cycle cannot be created through the write path,
// since category-service rejects a move that would create one, but a restored
// backup could contain one, and unbounded recursion on malformed data is a stack
// exhaustion rather than a wrong number.
func countDescendants(nodes []*categorypb.CategoryNode, target int64) int64 {
	// Flatten the nesting into a parent-to-children index first. The nesting itself
	// is not the authority: a node's position in the response and its parent_id can
	// disagree after a bad restore, and only parent_id describes the hierarchy the
	// deletion would actually affect.
	children := make(map[int64][]int64)

	var index func([]*categorypb.CategoryNode)
	index = func(list []*categorypb.CategoryNode) {
		for _, node := range list {
			cat := node.GetCategory()
			if cat == nil {
				continue
			}
			if parent := cat.GetParentId(); parent != 0 && parent != cat.GetId() {
				children[parent] = append(children[parent], cat.GetId())
			}
			index(node.GetChildren())
		}
	}
	index(nodes)

	var total int64
	seen := map[int64]struct{}{target: {}}
	var walk func(int64)
	walk = func(id int64) {
		for _, child := range children[id] {
			if _, cycle := seen[child]; cycle {
				continue
			}
			seen[child] = struct{}{}
			total++
			walk(child)
		}
	}
	walk(target)
	return total
}
