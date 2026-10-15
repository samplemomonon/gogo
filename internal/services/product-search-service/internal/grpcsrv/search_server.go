// Package grpcsrv exposes the search index over gRPC for internal callers. It is
// a thin adapter: it converts protobuf messages to and from the domain types and
// maps domain errors to gRPC status codes. No business rules live here.
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"product-search-service/gen/searchpb"
	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
)

// ProductSearchService is the behaviour this server needs from the service layer.
type ProductSearchService interface {
	Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error)
}

// Server adapts ProductSearchService to the generated server interface.
type Server struct {
	searchpb.UnimplementedProductSearchServiceServer

	svc ProductSearchService
	log *slog.Logger
}

// NewServer returns a gRPC server backed by svc.
func NewServer(svc ProductSearchService, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// SearchProducts implements searchpb.ProductSearchServiceServer.
func (s *Server) SearchProducts(
	ctx context.Context,
	req *searchpb.SearchProductsRequest,
) (*searchpb.SearchProductsResponse, error) {
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

	result, err := s.svc.Search(ctx, &model.SearchQuery{
		Query:         req.GetQuery(),
		CategoryID:    req.GetCategoryId(),
		Status:        statusFilter,
		MinPriceCents: optionalPrice(req.GetMinPriceCents()),
		MaxPriceCents: optionalPrice(req.GetMaxPriceCents()),
		Limit:         int(req.GetLimit()),
		Offset:        int(req.GetOffset()),
	})
	if err != nil {
		return nil, toStatus(err)
	}

	products := make([]*searchpb.SearchProduct, 0, len(result.Products))
	for _, p := range result.Products {
		products = append(products, productToProto(p))
	}
	return &searchpb.SearchProductsResponse{
		Products: products,
		Total:    result.Total,
		Limit:    int32(result.Limit),
		Offset:   int32(result.Offset),
		TookMs:   result.TookMS,
	}, nil
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
		// failure is not. An index that is behind is exactly that case.
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func productToProto(p *model.SearchProduct) *searchpb.SearchProduct {
	if p == nil {
		return nil
	}
	proto := &searchpb.SearchProduct{
		Id:          p.ID,
		CategoryId:  p.CategoryID,
		Name:        p.Name,
		Slug:        p.Slug,
		PriceCents:  p.PriceCents,
		Currency:    p.Currency,
		Stock:       int32(p.Stock),
		Status:      statusFromString(p.Status),
		Score:       p.Score,
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
	return proto
}

func statusToString(s searchpb.ProductStatus) string {
	switch s {
	case searchpb.ProductStatus_PRODUCT_STATUS_DRAFT:
		return string(model.StatusDraft)
	case searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return string(model.StatusActive)
	case searchpb.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return string(model.StatusArchived)
	default:
		return ""
	}
}

func statusFromString(s model.Status) searchpb.ProductStatus {
	switch s {
	case model.StatusDraft:
		return searchpb.ProductStatus_PRODUCT_STATUS_DRAFT
	case model.StatusActive:
		return searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE
	case model.StatusArchived:
		return searchpb.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return searchpb.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
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
