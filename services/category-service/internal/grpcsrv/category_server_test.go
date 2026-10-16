package grpcsrv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"category-service/gen/categorypb"
	"category-service/internal/apperror"
	"category-service/internal/model"
)

// stubService lets the adapter tests run without a database.
type stubService struct {
	categories []*model.Category
	tree       []*model.CategoryNode
	err        error

	gotID int64
}

func (s *stubService) CreateMerchant(_ context.Context, req *model.CreateCategoryRequest) (*model.Category, error) {
	return &model.Category{ID: 100, Name: req.Name, TenantID: "tenant-a"}, s.err
}

func (s *stubService) GetByID(_ context.Context, id int64) (*model.Category, error) {
	s.gotID = id
	if s.err != nil {
		return nil, s.err
	}
	for _, category := range s.categories {
		if category.ID == id {
			return category, nil
		}
	}
	return nil, errors.Join(apperror.ErrNotFound, errors.New("no such category"))
}

func (s *stubService) GetAll(context.Context) ([]*model.Category, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.categories, nil
}

func (s *stubService) GetTree(context.Context) ([]*model.CategoryNode, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tree, nil
}

func newTestServer(svc *stubService) *Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(svc, log)
}

func TestGetCategory(t *testing.T) {
	t.Parallel()

	description := "Gadgets"
	svc := &stubService{categories: []*model.Category{
		{ID: 1, Name: "Electronics", Description: &description},
	}}
	server := newTestServer(svc)

	resp, err := server.GetCategory(context.Background(), &categorypb.GetCategoryRequest{Id: 1})
	if err != nil {
		t.Fatalf("GetCategory: %v", err)
	}
	if resp.GetCategory().GetName() != "Electronics" {
		t.Errorf("name = %q, want Electronics", resp.GetCategory().GetName())
	}
	if resp.GetCategory().GetDescription() != "Gadgets" {
		t.Errorf("description = %q, want Gadgets", resp.GetCategory().GetDescription())
	}
	// A root category reports 0, since proto3 has no nullable scalar.
	if resp.GetCategory().GetParentId() != 0 {
		t.Errorf("parent id = %d, want 0 for a root", resp.GetCategory().GetParentId())
	}
}

func TestGetCategoryNotFound(t *testing.T) {
	t.Parallel()

	server := newTestServer(&stubService{})
	_, err := server.GetCategory(context.Background(), &categorypb.GetCategoryRequest{Id: 9})

	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %s, want NotFound", status.Code(err))
	}
}

func TestListCategories(t *testing.T) {
	t.Parallel()

	svc := &stubService{categories: []*model.Category{
		{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"},
	}}
	server := newTestServer(svc)

	t.Run("pages with a limit and offset", func(t *testing.T) {
		resp, err := server.ListCategories(
			context.Background(),
			&categorypb.ListCategoriesRequest{Limit: 2, Offset: 1},
		)
		if err != nil {
			t.Fatalf("ListCategories: %v", err)
		}
		if len(resp.GetCategories()) != 2 {
			t.Fatalf("got %d categories, want 2", len(resp.GetCategories()))
		}
		if resp.GetCategories()[0].GetId() != 2 {
			t.Errorf("first id = %d, want 2", resp.GetCategories()[0].GetId())
		}
		// The total counts matches, not the page.
		if resp.GetTotal() != 3 {
			t.Errorf("total = %d, want 3", resp.GetTotal())
		}
	})

	t.Run("returns everything when unpaged", func(t *testing.T) {
		resp, err := server.ListCategories(context.Background(), &categorypb.ListCategoriesRequest{})
		if err != nil {
			t.Fatalf("ListCategories: %v", err)
		}
		if len(resp.GetCategories()) != 3 {
			t.Errorf("got %d categories, want 3", len(resp.GetCategories()))
		}
	})

	t.Run("returns an empty page past the end", func(t *testing.T) {
		// Not an error: a client walking pages must be able to stop here.
		resp, err := server.ListCategories(
			context.Background(),
			&categorypb.ListCategoriesRequest{Offset: 99},
		)
		if err != nil {
			t.Fatalf("ListCategories: %v", err)
		}
		if len(resp.GetCategories()) != 0 {
			t.Errorf("got %d categories, want 0", len(resp.GetCategories()))
		}
		if resp.GetTotal() != 3 {
			t.Errorf("total = %d, want 3", resp.GetTotal())
		}
	})

	// offset and limit are int32 and reachable by any caller. The old arithmetic
	// computed start+limit in int32, which wraps: start=1, limit=MaxInt32 sums to
	// MinInt32, the comparison against total then passes, and the slice is taken
	// with a negative end index. That panics, and nothing in a gRPC server recovers
	// a panic, so a single request killed the process.
	//
	// These cases are the reachable ones. A limit of MaxInt32 with offset 0 does
	// not overflow (0+MaxInt32 fits), which is why it is not the interesting case.
	t.Run("does not overflow on extreme offset and limit", func(t *testing.T) {
		const maxInt32 = 1<<31 - 1

		cases := map[string]struct{ offset, limit int32 }{
			"max limit, offset one":     {offset: 1, limit: maxInt32},
			"max limit, offset two":     {offset: 2, limit: maxInt32},
			"both max":                  {offset: maxInt32, limit: maxInt32},
			"max limit, offset max-1":   {offset: maxInt32 - 1, limit: maxInt32},
			"limit and offset overflow": {offset: maxInt32 / 2, limit: maxInt32},
		}

		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				// A panic here is the failure being pinned down: it would propagate
				// out of the handler and take the server down.
				resp, err := server.ListCategories(context.Background(),
					&categorypb.ListCategoriesRequest{Offset: tc.offset, Limit: tc.limit})
				if err != nil {
					t.Fatalf("ListCategories: %v", err)
				}
				if len(resp.GetCategories()) > 3 {
					t.Errorf("got %d categories, want at most the 3 that exist", len(resp.GetCategories()))
				}
				if resp.GetTotal() != 3 {
					t.Errorf("total = %d, want 3", resp.GetTotal())
				}
			})
		}
	})

	t.Run("treats a negative offset as the beginning", func(t *testing.T) {
		resp, err := server.ListCategories(context.Background(),
			&categorypb.ListCategoriesRequest{Offset: -5, Limit: 2})
		if err != nil {
			t.Fatalf("ListCategories: %v", err)
		}
		if len(resp.GetCategories()) != 2 {
			t.Fatalf("got %d categories, want 2", len(resp.GetCategories()))
		}
		if resp.GetCategories()[0].GetId() != 1 {
			t.Errorf("first id = %d, want 1: a negative offset must not wrap", resp.GetCategories()[0].GetId())
		}
	})

	t.Run("an empty result set is empty for every offset", func(t *testing.T) {
		empty := newTestServer(&stubService{categories: nil})
		resp, err := empty.ListCategories(context.Background(),
			&categorypb.ListCategoriesRequest{Offset: 1, Limit: 1<<31 - 1})
		if err != nil {
			t.Fatalf("ListCategories: %v", err)
		}
		if len(resp.GetCategories()) != 0 {
			t.Errorf("got %d categories, want 0", len(resp.GetCategories()))
		}
	})
}

// paginate is the function that had to change, so it is tested directly as well as
// through the handler: the panic depended on the arithmetic, not on the wiring.
func TestPaginate(t *testing.T) {
	t.Parallel()

	const maxInt32 = 1<<31 - 1

	t.Run("bounds are always inside the slice", func(t *testing.T) {
		offsets := []int32{0, 1, 2, 5, 99, maxInt32 - 1, maxInt32, -1, -maxInt32}
		limits := []int32{0, -1, 1, 2, 3, 99, maxInt32 - 1, maxInt32, -maxInt32}

		for _, total := range []int{0, 1, 3, 10, 1000} {
			for _, offset := range offsets {
				for _, limit := range limits {
					got := paginate(total, offset, limit)
					if got.start < 0 || got.start > total {
						t.Errorf("total=%d offset=%d limit=%d: start=%d out of range",
							total, offset, limit, got.start)
						continue
					}
					if got.end < got.start || got.end > total {
						t.Errorf("total=%d offset=%d limit=%d: end=%d outside [%d, %d]",
							total, offset, limit, got.end, got.start, total)
					}
				}
			}
		}
	})

	t.Run("pages as asked", func(t *testing.T) {
		cases := []struct {
			total         int
			offset, limit int32
			wantStart     int
			wantEnd       int
		}{
			{total: 10, offset: 0, limit: 3, wantStart: 0, wantEnd: 3},
			{total: 10, offset: 3, limit: 4, wantStart: 3, wantEnd: 7},
			{total: 10, offset: 9, limit: 4, wantStart: 9, wantEnd: 10},
			{total: 10, offset: 99, limit: 4, wantStart: 10, wantEnd: 10},
			{total: 10, offset: 0, limit: 0, wantStart: 0, wantEnd: 10},
			{total: 10, offset: 0, limit: -1, wantStart: 0, wantEnd: 10},
			{total: 10, offset: -3, limit: 2, wantStart: 0, wantEnd: 2},
			{total: 0, offset: 5, limit: 5, wantStart: 0, wantEnd: 0},
		}
		for _, tc := range cases {
			got := paginate(tc.total, tc.offset, tc.limit)
			if got.start != tc.wantStart || got.end != tc.wantEnd {
				t.Errorf("paginate(%d, %d, %d) = [%d, %d), want [%d, %d)",
					tc.total, tc.offset, tc.limit,
					got.start, got.end, tc.wantStart, tc.wantEnd)
			}
		}
	})

	t.Run("caps an oversized limit", func(t *testing.T) {
		got := paginate(maxInt32, 0, maxInt32)
		if got.size() > maxPageSize {
			t.Errorf("page holds %d rows, want at most the cap of %d", got.size(), maxPageSize)
		}
		if got.start != 0 || got.end != maxPageSize {
			t.Errorf("bounds = [%d, %d), want [0, %d)", got.start, got.end, maxPageSize)
		}
	})
}

func TestCategoryExists(t *testing.T) {
	t.Parallel()

	parentID := int64(1)
	svc := &stubService{categories: []*model.Category{
		{ID: 1, Name: "Electronics"},
		{ID: 2, Name: "Smartphones", ParentID: &parentID},
	}}
	server := newTestServer(svc)

	t.Run("splits ids into existing and missing", func(t *testing.T) {
		resp, err := server.CategoryExists(
			context.Background(),
			&categorypb.CategoryExistsRequest{Ids: []int64{1, 2, 99}},
		)
		if err != nil {
			t.Fatalf("CategoryExists: %v", err)
		}
		if len(resp.GetExists()) != 2 {
			t.Errorf("exists = %v, want two ids", resp.GetExists())
		}
		if len(resp.GetMissing()) != 1 || resp.GetMissing()[0] != 99 {
			t.Errorf("missing = %v, want [99]", resp.GetMissing())
		}
	})

	t.Run("de-duplicates and drops non-positive ids", func(t *testing.T) {
		resp, err := server.CategoryExists(
			context.Background(),
			&categorypb.CategoryExistsRequest{Ids: []int64{1, 1, 0, -5}},
		)
		if err != nil {
			t.Fatalf("CategoryExists: %v", err)
		}
		if len(resp.GetExists()) != 1 || resp.GetExists()[0] != 1 {
			t.Errorf("exists = %v, want [1]", resp.GetExists())
		}
	})

	t.Run("handles an empty request", func(t *testing.T) {
		resp, err := server.CategoryExists(context.Background(), &categorypb.CategoryExistsRequest{})
		if err != nil {
			t.Fatalf("CategoryExists: %v", err)
		}
		if len(resp.GetExists()) != 0 || len(resp.GetMissing()) != 0 {
			t.Errorf("got %v/%v, want two empty lists", resp.GetExists(), resp.GetMissing())
		}
	})

	t.Run("does not report a fault as missing", func(t *testing.T) {
		// A storage failure must not be cached by the caller as "does not exist".
		failing := newTestServer(&stubService{err: errors.New("connection reset")})

		_, err := failing.CategoryExists(
			context.Background(),
			&categorypb.CategoryExistsRequest{Ids: []int64{1}},
		)
		if status.Code(err) != codes.Internal {
			t.Errorf("code = %s, want Internal", status.Code(err))
		}
	})
}

func TestGetCategoryTree(t *testing.T) {
	t.Parallel()

	parentID := int64(1)
	svc := &stubService{
		tree: []*model.CategoryNode{{
			Category: model.Category{ID: 1, Name: "Electronics"},
			Children: []*model.CategoryNode{{
				Category: model.Category{ID: 2, Name: "Smartphones", ParentID: &parentID},
				// A non-nil empty slice, so the wire form is [] rather than null.
				Children: []*model.CategoryNode{},
			}},
		}},
	}
	server := newTestServer(svc)

	resp, err := server.GetCategoryTree(context.Background(), &categorypb.GetCategoryTreeRequest{})
	if err != nil {
		t.Fatalf("GetCategoryTree: %v", err)
	}
	if len(resp.GetRoots()) != 1 {
		t.Fatalf("got %d roots, want 1", len(resp.GetRoots()))
	}
	root := resp.GetRoots()[0]
	if len(root.GetChildren()) != 1 {
		t.Fatalf("root has %d children, want 1", len(root.GetChildren()))
	}
	child := root.GetChildren()[0].GetCategory()
	if child.GetName() != "Smartphones" {
		t.Errorf("child name = %q, want Smartphones", child.GetName())
	}
}

func TestToStatus(t *testing.T) {
	t.Parallel()

	cases := map[error]codes.Code{
		errors.Join(apperror.ErrNotFound, errors.New("gone")):          codes.NotFound,
		errors.Join(apperror.ErrInvalidInput, errors.New("bad")):       codes.InvalidArgument,
		errors.Join(apperror.ErrCircularReference, errors.New("loop")): codes.FailedPrecondition,
		errors.New("something unexpected"):                             codes.Internal,
	}

	for err, want := range cases {
		if got := status.Code(toStatus(err)); got != want {
			t.Errorf("toStatus(%v) = %s, want %s", err, got, want)
		}
	}

	if toStatus(nil) != nil {
		t.Error("toStatus(nil) is not nil")
	}
}
