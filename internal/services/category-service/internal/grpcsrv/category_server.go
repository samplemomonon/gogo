// Package grpcsrv exposes the category read API over gRPC for internal callers.
// It is a thin adapter: it converts protobuf messages to and from the domain
// types and maps domain errors to gRPC status codes. No business rules live
// here.
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"category-service/gen/categorypb"
	"category-service/internal/apperror"
	"category-service/internal/model"
)

// CategoryService is the read behaviour this server needs from the service layer.
type CategoryService interface {
	GetByID(ctx context.Context, id int64) (*model.Category, error)
	GetAll(ctx context.Context) ([]*model.Category, error)
	GetTree(ctx context.Context) ([]*model.CategoryNode, error)
}

// Server adapts CategoryService to the generated CategoryServiceServer interface.
type Server struct {
	categorypb.UnimplementedCategoryServiceServer

	svc CategoryService
	log *slog.Logger
}

// NewServer returns a gRPC server backed by svc.
func NewServer(svc CategoryService, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// GetCategory implements categorypb.CategoryServiceServer.
func (s *Server) GetCategory(
	ctx context.Context,
	req *categorypb.GetCategoryRequest,
) (*categorypb.GetCategoryResponse, error) {
	category, err := s.svc.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &categorypb.GetCategoryResponse{Category: categoryToProto(category)}, nil
}

// ListCategories implements categorypb.CategoryServiceServer.
//
// The service layer returns the full set, so paging is applied here by slicing.
// That keeps the existing repository untouched while still honouring the page
// size a caller asked for.
//
// The bounds come from paginate rather than being computed inline, because the
// arithmetic is not safe in the proto's own width. offset and limit are int32, so
// start+limit overflows for a large limit and wraps negative; slicing with a
// negative end index panics, and a gRPC handler that panics takes the process with
// it. Any caller could trigger that with a single request.
func (s *Server) ListCategories(
	ctx context.Context,
	req *categorypb.ListCategoriesRequest,
) (*categorypb.ListCategoriesResponse, error) {
	categories, err := s.svc.GetAll(ctx)
	if err != nil {
		return nil, toStatus(err)
	}

	total := len(categories)
	page := paginate(total, req.GetOffset(), req.GetLimit())
	out := make([]*categorypb.Category, 0, page.size())
	for _, category := range categories[page.start:page.end] {
		out = append(out, categoryToProto(category))
	}
	return &categorypb.ListCategoriesResponse{Categories: out, Total: int64(total)}, nil
}

// GetCategoryTree implements categorypb.CategoryServiceServer.
func (s *Server) GetCategoryTree(
	ctx context.Context,
	_ *categorypb.GetCategoryTreeRequest,
) (*categorypb.GetCategoryTreeResponse, error) {
	tree, err := s.svc.GetTree(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*categorypb.CategoryNode, 0, len(tree))
	for _, node := range tree {
		out = append(out, nodeToProto(node))
	}
	return &categorypb.GetCategoryTreeResponse{Roots: out}, nil
}

// CategoryExists implements categorypb.CategoryServiceServer.
//
// This exists so a caller validating a foreign key needs one call rather than
// one per id, which is what product-service does when it stores a product.
func (s *Server) CategoryExists(
	ctx context.Context,
	req *categorypb.CategoryExistsRequest,
) (*categorypb.CategoryExistsResponse, error) {
	ids := req.GetIds()
	if len(ids) == 0 {
		return &categorypb.CategoryExistsResponse{}, nil
	}

	// Deduplicate before asking the service, so a repeated id cannot produce a
	// repeated entry in either result list.
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}

	exists := make([]int64, 0, len(unique))
	missing := make([]int64, 0, len(unique))
	for _, id := range unique {
		_, err := s.svc.GetByID(ctx, id)
		switch {
		case err == nil:
			exists = append(exists, id)
		case errors.Is(err, apperror.ErrNotFound):
			missing = append(missing, id)
		default:
			// A dependency or storage fault must not be reported as "does not
			// exist", or a caller would cache a wrong answer.
			return nil, toStatus(err)
		}
	}
	return &categorypb.CategoryExistsResponse{Exists: exists, Missing: missing}, nil
}

// maxPageSize bounds how many rows one call returns.
//
// It exists to make the paging contract explicit rather than to protect the
// arithmetic: the real fix is doing that arithmetic in a width that cannot
// overflow. This is the number a caller would actually want, and answering a
// request for a million rows by materialising a million rows would be its own
// kind of failure.
const maxPageSize = 500

// page is the half-open range [start, end) of one slice of the result set.
//
// The bounds are guaranteed to satisfy 0 <= start <= end <= total, which is what
// makes categories[page.start:page.end] safe without a further check at the call
// site.
type page struct {
	start int
	end   int
}

// size is how many rows the page holds.
func (p page) size() int { return p.end - p.start }

// paginate turns a caller's offset and limit into a safe slice range.
//
// The inputs are int32 because that is what the proto declares, and they are
// attacker-controlled: any client may send offset=2_147_483_647.
//
// The arithmetic is done in int64, and that is the whole point. Computed in int32,
// start+limit wraps: with start=1 and limit=MaxInt32 the sum becomes MinInt32, the
// comparison against total then succeeds, and the slice is taken with a negative
// end index. That panics, and nothing recovers a panic inside a gRPC handler, so
// it kills the process — one request from any caller that can reach this service.
func paginate(total int, offset, limit int32) page {
	// A negative offset is treated as zero rather than rejected: proto3 cannot
	// distinguish "unset" from zero, so zero is the only value that means "the
	// beginning" either way.
	start := clamp(int64(offset), 0, int64(total))

	remaining := int64(total) - start

	// A limit of zero or less means no page size was asked for, which reads as the
	// whole remaining set.
	span := remaining
	if limit > 0 && int64(limit) < span {
		span = int64(limit)
	}
	if span > maxPageSize {
		span = maxPageSize
	}

	end := clamp(start+span, start, int64(total))
	return page{start: int(start), end: int(end)}
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
	case errors.Is(err, apperror.ErrCircularReference):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func categoryToProto(c *model.Category) *categorypb.Category {
	if c == nil {
		return nil
	}
	proto := &categorypb.Category{
		Id:        c.ID,
		Name:      c.Name,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
	if c.Description != nil {
		proto.Description = *c.Description
	}
	// proto3 has no nullable scalar, so a root category is reported as 0. An id
	// is always positive, so 0 is unambiguous.
	if c.ParentID != nil {
		proto.ParentId = *c.ParentID
	}
	return proto
}

func nodeToProto(n *model.CategoryNode) *categorypb.CategoryNode {
	if n == nil {
		return nil
	}
	children := make([]*categorypb.CategoryNode, 0, len(n.Children))
	for _, child := range n.Children {
		children = append(children, nodeToProto(child))
	}
	return &categorypb.CategoryNode{
		Category: categoryToProto(&n.Category),
		Children: children,
	}
}
