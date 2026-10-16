package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"category-service/internal/apperror"
	"category-service/internal/model"
)

func TestBuildTree(t *testing.T) {
	t.Parallel()

	t.Run("nests children under parents", func(t *testing.T) {
		t.Parallel()

		categories := []*model.Category{
			{ID: 1, Name: "Electronics"},
			{ID: 2, Name: "Smartphones", ParentID: int64ptr(1)},
			{ID: 3, Name: "iPhone", ParentID: int64ptr(2)},
		}

		roots := BuildTree(categories)

		if len(roots) != 1 {
			t.Fatalf("got %d roots, want 1", len(roots))
		}
		if roots[0].Name != "Electronics" {
			t.Errorf("root name = %q, want Electronics", roots[0].Name)
		}
		if got := len(roots[0].Children); got != 1 {
			t.Fatalf("Electronics has %d children, want 1", got)
		}

		smartphones := roots[0].Children[0]
		if got := len(smartphones.Children); got != 1 {
			t.Fatalf("Smartphones has %d children, want 1", got)
		}
		if smartphones.Children[0].Name != "iPhone" {
			t.Errorf("leaf name = %q, want iPhone", smartphones.Children[0].Name)
		}
	})

	t.Run("keeps multiple roots separate", func(t *testing.T) {
		t.Parallel()

		roots := BuildTree([]*model.Category{
			{ID: 1, Name: "Books"},
			{ID: 2, Name: "Music"},
		})

		if len(roots) != 2 {
			t.Fatalf("got %d roots, want 2", len(roots))
		}
	})

	t.Run("treats a missing parent as a root", func(t *testing.T) {
		t.Parallel()

		// The parent is absent from the list, so the child must not disappear.
		roots := BuildTree([]*model.Category{
			{ID: 7, Name: "Orphan", ParentID: int64ptr(99)},
		})

		if len(roots) != 1 {
			t.Fatalf("got %d roots, want 1", len(roots))
		}
		if roots[0].ID != 7 {
			t.Errorf("root id = %d, want 7", roots[0].ID)
		}
	})

	t.Run("handles an empty list", func(t *testing.T) {
		t.Parallel()

		roots := BuildTree(nil)
		if roots == nil {
			t.Fatal("want an empty slice, got nil")
		}
		if len(roots) != 0 {
			t.Fatalf("got %d roots, want 0", len(roots))
		}
	})
}

// stubRepo is a hand written CategoryRepository for service tests.
type stubRepo struct {
	existing   map[int64]*model.Category
	created    *model.Category
	update     *model.Category
	ancestors  []*model.Category
	children   []*model.Category
	descendant int
	cycle      bool

	gotCreate *model.CreateCategoryRequest
	gotUpdate *model.UpdateCategoryRequest
	gotDelete int64
	deleted   bool
}

func newStubRepo(ids ...int64) *stubRepo {
	repo := &stubRepo{existing: make(map[int64]*model.Category, len(ids))}
	for _, id := range ids {
		repo.existing[id] = &model.Category{ID: id, CreatedAt: time.Now()}
	}
	return repo
}

func (s *stubRepo) Create(_ context.Context, req *model.CreateCategoryRequest) (*model.Category, error) {
	s.gotCreate = req
	return s.created, nil
}

func (s *stubRepo) GetByID(_ context.Context, id int64) (*model.Category, error) {
	if c, ok := s.existing[id]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("%w: category %d", apperror.ErrNotFound, id)
}

func (s *stubRepo) GetAll(context.Context) ([]*model.Category, error) {
	out := make([]*model.Category, 0, len(s.existing))
	for _, id := range []int64{1, 2, 3} {
		if c, ok := s.existing[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *stubRepo) GetChildren(context.Context, int64) ([]*model.Category, error) {
	return s.children, nil
}

func (s *stubRepo) GetAncestors(context.Context, int64) ([]*model.Category, error) {
	return s.ancestors, nil
}

func (s *stubRepo) Update(_ context.Context, _ int64, req *model.UpdateCategoryRequest) (*model.Category, error) {
	s.gotUpdate = req
	return s.update, nil
}

func (s *stubRepo) Delete(_ context.Context, id int64) error {
	s.gotDelete = id
	if _, ok := s.existing[id]; !ok {
		return fmt.Errorf("%w: category %d", apperror.ErrNotFound, id)
	}
	s.deleted = true
	return nil
}

func (s *stubRepo) WouldCreateCircularReference(context.Context, int64, int64) (bool, error) {
	return s.cycle, nil
}

func (s *stubRepo) CountDescendants(context.Context, int64) (int, error) {
	return s.descendant, nil
}

func int64ptr(v int64) *int64 { return &v }

// decodeUpdate builds an update body the way the JSON decoder would, recording
// which keys were present. A struct literal leaves the presence set empty, which
// the service correctly reads as "do not touch this field".
func decodeUpdate(t *testing.T, body map[string]any) *model.UpdateCategoryRequest {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal update body: %v", err)
	}
	var req model.UpdateCategoryRequest
	if err := json.Unmarshal(encoded, &req); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	return &req
}

func TestServiceUpdate_RejectsSelfParent(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo(1))

	_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"parent_id": 1}))

	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestServiceUpdate_RejectsCycle(t *testing.T) {
	t.Parallel()

	// The new parent exists but is a descendant, which would close a loop.
	repo := newStubRepo(1, 2)
	repo.cycle = true
	svc := NewCategoryService(repo)

	_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"parent_id": 2}))

	if !errors.Is(err, apperror.ErrCircularReference) {
		t.Fatalf("err = %v, want ErrCircularReference", err)
	}
}

func TestServiceUpdate_RejectsMissingParent(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo(1))

	_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"parent_id": 404}))

	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestServiceUpdate_RejectsNonPositiveParent(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo(1))

	for _, parent := range []int{0, -1} {
		_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"parent_id": parent}))
		if !errors.Is(err, apperror.ErrInvalidInput) {
			t.Errorf("parent_id %d: err = %v, want ErrInvalidInput", parent, err)
		}
	}
}

func TestServiceUpdate_RejectsNullName(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo(1))

	// The column is NOT NULL, so clearing the name must be refused.
	_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"name": nil}))

	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestServiceUpdate_AllowsDetachingParent(t *testing.T) {
	t.Parallel()

	// A null parent_id moves the category to the top level, which is valid.
	repo := newStubRepo(1)
	svc := NewCategoryService(repo)

	if _, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"parent_id": nil})); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if repo.gotUpdate == nil || !repo.gotUpdate.HasParentID() {
		t.Fatal("parent_id should be marked as present")
	}
}

func TestServiceUpdate_IgnoresAbsentFields(t *testing.T) {
	t.Parallel()

	// A body naming only description must not clear the name.
	repo := newStubRepo(1)
	repo.update = &model.Category{ID: 1, Name: "kept"}
	svc := NewCategoryService(repo)

	_, err := svc.Update(context.Background(), 1, decodeUpdate(t, map[string]any{"description": "d"}))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if repo.gotUpdate.HasName() {
		t.Error("name should not be marked present")
	}
	if !repo.gotUpdate.HasDescription() {
		t.Error("description should be marked present")
	}
}

func TestServiceUpdate_MissingCategoryIsNotFound(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo())

	_, err := svc.Update(context.Background(), 99, decodeUpdate(t, map[string]any{"name": "new"}))

	if !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServiceCreate_RejectsBlankName(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo())

	for _, name := range []string{"", "   "} {
		if _, err := svc.Create(context.Background(), &model.CreateCategoryRequest{Name: name}); !errors.Is(err, apperror.ErrInvalidInput) {
			t.Errorf("name %q: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestServiceCreate_RejectsOverlongName(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo())
	long := make([]byte, maxNameLength+1)
	for i := range long {
		long[i] = 'a'
	}

	_, err := svc.Create(context.Background(), &model.CreateCategoryRequest{Name: string(long)})

	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestServiceDelete_PropagatesNotFound(t *testing.T) {
	t.Parallel()

	svc := NewCategoryService(newStubRepo())

	if err := svc.Delete(context.Background(), 42); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServiceGetTree_UsesRepositoryListing(t *testing.T) {
	t.Parallel()

	repo := newStubRepo(1, 2)
	repo.existing[2].ParentID = int64ptr(1)
	svc := NewCategoryService(repo)

	tree, err := svc.GetTree(context.Background())
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("got %d roots, want 1", len(tree))
	}
	if len(tree[0].Children) != 1 {
		t.Errorf("root has %d children, want 1", len(tree[0].Children))
	}
}
