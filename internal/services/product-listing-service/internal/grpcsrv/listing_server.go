// Package grpcsrv exposes the read model over gRPC for internal callers. It is a
// thin adapter: it converts protobuf messages to and from the domain types and
// maps domain errors to gRPC status codes. No business rules live here.
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ruangbercerita182-tech/product-listing-service/gen/listingpb"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// ProductListingService is the behaviour this server needs from the service
// layer.
type ProductListingService interface {
	GetByID(ctx context.Context, id int64, includeVariants bool) (*model.Product, error)
	List(ctx context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error)
	ListVariants(ctx context.Context, productID int64) ([]*model.Variant, error)
}

// Server adapts ProductListingService to the generated server interface.
type Server struct {
	listingpb.UnimplementedProductListingServiceServer

	svc ProductListingService
	log *slog.Logger
}

// NewServer returns a gRPC server backed by svc.
func NewServer(svc ProductListingService, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// GetProduct implements listingpb.ProductListingServiceServer.
func (s *Server) GetProduct(
	ctx context.Context,
	req *listingpb.GetProductRequest,
) (*listingpb.GetProductResponse, error) {
	product, err := s.svc.GetByID(ctx, req.GetId(), req.GetIncludeVariants())
	if err != nil {
		return nil, toStatus(err)
	}
	return &listingpb.GetProductResponse{Product: productToProto(product)}, nil
}

// ListProducts implements listingpb.ProductListingServiceServer.
func (s *Server) ListProducts(
	ctx context.Context,
	req *listingpb.ListProductsRequest,
) (*listingpb.ListProductsResponse, error) {
	// An unspecified status means "no filter". The distinction matters: treating
	// the zero value as draft would silently hide every active product.
	var statusFilter *model.Status
	if raw := statusToString(req.GetStatus()); raw != "" {
		parsed, err := model.ParseStatus(raw)
		if err != nil {
			return nil, toStatus(err)
		}
		statusFilter = &parsed
	}

	// A search term belongs to product-search-service. The field is still in the
	// contract as a reserved number so an old client gets a clear InvalidArgument
	// rather than a silently unfiltered result set.
	if req.GetQuery() != "" {
		return nil, status.Errorf(codes.InvalidArgument,
			"query is not supported by %s: free-text search moved to product-search-service, "+
				"use productsearch.v1.ProductSearchService/SearchProducts",
			"productlisting.v1.ProductListingService")
	}

	result, err := s.svc.List(ctx, &model.ListProductsQuery{
		CategoryID:      req.GetCategoryId(),
		Status:          statusFilter,
		MinPriceCents:   optionalPrice(req.GetMinPriceCents()),
		MaxPriceCents:   optionalPrice(req.GetMaxPriceCents()),
		Limit:           int(req.GetLimit()),
		Offset:          int(req.GetOffset()),
		IncludeVariants: req.GetIncludeVariants(),
	})
	if err != nil {
		return nil, toStatus(err)
	}

	products := make([]*listingpb.Product, 0, len(result.Products))
	for _, p := range result.Products {
		products = append(products, productToProto(p))
	}
	return &listingpb.ListProductsResponse{Products: products, Total: result.Total}, nil
}

// optionalPrice turns a proto price bound into the query's presence form.
//
// A limitation worth stating plainly: min_price_cents and max_price_cents are
// declared as bare proto3 int64, which has implicit presence. The generated getter
// cannot tell an unset field from one explicitly set to 0, so over gRPC a bound of
// zero still reads as "no filter". Adding `optional` to the two fields would give
// them explicit presence and fix it on the wire; that is a contract change and a
// deliberate decision, so it is not made here. Over HTTP the distinction is now
// carried correctly, which is where the silent over-return was reported.
func optionalPrice(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

// ListVariants implements listingpb.ProductListingServiceServer.
func (s *Server) ListVariants(
	ctx context.Context,
	req *listingpb.ListVariantsRequest,
) (*listingpb.ListVariantsResponse, error) {
	variants, err := s.svc.ListVariants(ctx, req.GetProductId())
	if err != nil {
		return nil, toStatus(err)
	}

	out := make([]*listingpb.Variant, 0, len(variants))
	for _, v := range variants {
		out = append(out, variantToProto(v))
	}
	return &listingpb.ListVariantsResponse{Variants: out}, nil
}

// toStatus maps a domain error to a gRPC status so callers can branch on a code
// instead of parsing a message. Anything unrecognised becomes Internal, and the
// real cause stays in the log rather than travelling to a peer.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apperror.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, apperror.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, apperror.ErrUnavailable):
		// Unavailable tells the caller this is worth retrying, which a validation
		// failure is not. A read model that is behind is exactly that case.
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func productToProto(p *model.Product) *listingpb.Product {
	if p == nil {
		return nil
	}
	proto := &listingpb.Product{
		Id:          p.ID,
		CategoryId:  p.CategoryID,
		Name:        p.Name,
		Slug:        p.Slug,
		PriceCents:  p.PriceCents,
		Currency:    p.Currency,
		Stock:       int32(p.Stock),
		Status:      statusFromString(p.Status),
		CreatedAt:   timestamppb.New(p.CreatedAt),
		UpdatedAt:   timestamppb.New(p.UpdatedAt),
		ProjectedAt: timestamppb.New(p.ProjectedAt),
	}
	if p.Description != nil {
		proto.Description = *p.Description
	}
	if p.Brand != nil {
		proto.Brand = *p.Brand
	}
	if p.PrimaryMediaID != nil {
		proto.PrimaryMediaId = *p.PrimaryMediaID
	}
	proto.Variants = make([]*listingpb.Variant, 0, len(p.Variants))
	for _, v := range p.Variants {
		proto.Variants = append(proto.Variants, variantToProto(v))
	}
	return proto
}

func variantToProto(v *model.Variant) *listingpb.Variant {
	if v == nil {
		return nil
	}
	proto := &listingpb.Variant{
		Id:         v.ID,
		ProductId:  v.ProductID,
		Sku:        v.SKU,
		Name:       v.Name,
		PriceCents: v.PriceCents,
		Currency:   v.Currency,
		Stock:      int32(v.Stock),
		CreatedAt:  timestamppb.New(v.CreatedAt),
		UpdatedAt:  timestamppb.New(v.UpdatedAt),
	}
	if v.MediaID != nil {
		proto.MediaId = *v.MediaID
	}
	return proto
}

func statusToString(s listingpb.ProductStatus) string {
	switch s {
	case listingpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return string(model.StatusDraft)
	case listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return string(model.StatusActive)
	case listingpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return string(model.StatusArchived)
	default:
		return ""
	}
}

func statusFromString(s model.Status) listingpb.ProductStatus {
	switch s {
	case model.StatusDraft:
		return listingpb.ProductStatus_PRODUCT_STATUS_DRAFT
	case model.StatusActive:
		return listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE
	case model.StatusArchived:
		return listingpb.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return listingpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}
