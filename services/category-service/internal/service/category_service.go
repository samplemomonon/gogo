// Package service holds the category business rules. It validates input,
// enforces hierarchy invariants, and translates persistence errors into
// domain errors without knowing about HTTP.
package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"category-service/internal/apperror"
	"category-service/internal/model"
	"category-service/internal/tenant"
)

// maxNameLength mirrors the VARCHAR(255) limit of the categories.name column.
const maxNameLength = 255

// CategoryRepository is the subset of persistence behaviour the service needs.
// Declaring it here keeps the service testable with a stub.
type CategoryRepository interface {
	Create(ctx context.Context, req *model.CreateCategoryRequest) (*model.Category, error)
	GetByID(ctx context.Context, id int64) (*model.Category, error)
	GetAll(ctx context.Context) ([]*model.Category, error)
	GetChildren(ctx context.Context, parentID int64) ([]*model.Category, error)
	GetAncestors(ctx context.Context, id int64) ([]*model.Category, error)
	Update(ctx context.Context, id int64, req *model.UpdateCategoryRequest) (*model.Category, error)
	Delete(ctx context.Context, id int64) error
	WouldCreateCircularReference(ctx context.Context, categoryID, newParentID int64) (bool, error)
	CountDescendants(ctx context.Context, id int64) (int, error)
}

// CategoryService implements the category business rules.
type CategoryService struct {
	repo CategoryRepository
}

// NewCategoryService returns a service backed by repo.
func NewCategoryService(repo CategoryRepository) *CategoryService {
	return &CategoryService{repo: repo}
}

// Create validates the request and stores a new category.
func (s *CategoryService) Create(
	ctx context.Context,
	req *model.CreateCategoryRequest,
) (*model.Category, error) {
	// This API is the global-category write path. Do not let an incidental tenant
	// header turn an administrator-managed category into a merchant-local one.
	ctx = tenant.WithID(ctx, "")
	req.Name = strings.TrimSpace(req.Name)
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if req.ParentID != nil {
		// A hierarchy cannot cross global and merchant ownership boundaries.
		parent, err := s.repo.GetByID(ctx, *req.ParentID)
		if err != nil || parent.TenantID != tenant.FromContext(ctx) {
			return nil, fmt.Errorf("%w: parent category %d does not exist", apperror.ErrInvalidInput, *req.ParentID)
		}
	}
	return s.repo.Create(ctx, req)
}

// CreateMerchant creates a category in the caller's merchant scope. Unlike the
// administrator REST API, this path requires an authenticated tenant context.
func (s *CategoryService) CreateMerchant(
	ctx context.Context,
	req *model.CreateCategoryRequest,
) (*model.Category, error) {
	merchantID := tenant.FromContext(ctx)
	if merchantID == "" {
		return nil, fmt.Errorf("%w: merchant scope is required", apperror.ErrInvalidInput)
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if req.ParentID != nil {
		parent, err := s.repo.GetByID(ctx, *req.ParentID)
		if err != nil || parent.TenantID != merchantID {
			return nil, fmt.Errorf("%w: parent category must belong to this merchant", apperror.ErrInvalidInput)
		}
	}
	return s.repo.Create(ctx, req)
}

// GetByID returns a single category.
func (s *CategoryService) GetByID(ctx context.Context, id int64) (*model.Category, error) {
	return s.repo.GetByID(ctx, id)
}

// GetAll returns all categories as a flat list.
func (s *CategoryService) GetAll(ctx context.Context) ([]*model.Category, error) {
	return s.repo.GetAll(ctx)
}

// GetTree returns all categories assembled into a nested structure.
func (s *CategoryService) GetTree(ctx context.Context) ([]*model.CategoryNode, error) {
	categories, err := s.repo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	return BuildTree(categories), nil
}

// GetChildren returns the direct children of parentID.
func (s *CategoryService) GetChildren(ctx context.Context, parentID int64) ([]*model.Category, error) {
	if err := s.requireCategory(ctx, parentID); err != nil {
		return nil, err
	}
	return s.repo.GetChildren(ctx, parentID)
}

// GetAncestors returns the chain from a category's parent up to the root.
func (s *CategoryService) GetAncestors(ctx context.Context, id int64) ([]*model.Category, error) {
	if err := s.requireCategory(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.GetAncestors(ctx, id)
}

// Update modifies an existing category, rejecting changes that would break the
// hierarchy invariants.
func (s *CategoryService) Update(
	ctx context.Context,
	id int64,
	req *model.UpdateCategoryRequest,
) (*model.Category, error) {
	if err := s.requireCategory(ctx, id); err != nil {
		return nil, err
	}

	// A null name is rejected: the column is NOT NULL, so let it through only
	// as a real value.
	if req.HasName() {
		if req.Name == nil {
			return nil, fmt.Errorf("%w: name must not be null", apperror.ErrInvalidInput)
		}
		if err := validateName(*req.Name); err != nil {
			return nil, err
		}
	}

	// A null parent_id detaches the category from its parent, which is valid.
	// A non-nil one must point at an existing category without forming a cycle.
	if req.HasParentID() && req.ParentID != nil {
		newParentID := *req.ParentID
		switch {
		case newParentID == id:
			return nil, fmt.Errorf("%w: category %d cannot be its own parent", apperror.ErrInvalidInput, id)
		case newParentID <= 0:
			return nil, fmt.Errorf("%w: parent id must be positive, got %d", apperror.ErrInvalidInput, newParentID)
		}

		parent, err := s.repo.GetByID(ctx, newParentID)
		if err != nil || parent.TenantID != tenant.FromContext(ctx) {
			return nil, fmt.Errorf("%w: parent category %d does not exist", apperror.ErrInvalidInput, newParentID)
		}

		wouldCycle, err := s.repo.WouldCreateCircularReference(ctx, id, newParentID)
		if err != nil {
			return nil, err
		}
		if wouldCycle {
			return nil, fmt.Errorf(
				"%w: category %d is a descendant of %d", apperror.ErrCircularReference, newParentID, id,
			)
		}
	}

	return s.repo.Update(ctx, id, req)
}

// Delete removes a category and, by way of the schema cascade, its subtree.
func (s *CategoryService) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// DeletionImpact reports how many descendants a delete would remove, so the
// caller can decide whether to confirm.
func (s *CategoryService) DeletionImpact(ctx context.Context, id int64) (int, error) {
	if err := s.requireCategory(ctx, id); err != nil {
		return 0, err
	}
	return s.repo.CountDescendants(ctx, id)
}

// requireCategory fails with apperror.ErrNotFound when the id does not exist.
// Any other failure (connection lost, bad SQL) propagates untouched so it is
// reported as a server fault rather than a 404.
func (s *CategoryService) requireCategory(ctx context.Context, id int64) error {
	_, err := s.repo.GetByID(ctx, id)
	return err
}

func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	switch {
	case trimmed == "":
		return fmt.Errorf("%w: name is required", apperror.ErrInvalidInput)
	case utf8.RuneCountInString(trimmed) > maxNameLength:
		return fmt.Errorf(
			"%w: name must be at most %d characters, got %d",
			apperror.ErrInvalidInput, maxNameLength, utf8.RuneCountInString(trimmed),
		)
	}
	return nil
}

// BuildTree converts a flat category list into a nested structure. Categories
// whose parent is absent from the list are treated as roots so a partial read
// never hides data.
func BuildTree(categories []*model.Category) []*model.CategoryNode {
	nodes := make(map[int64]*model.CategoryNode, len(categories))

	// First pass: allocate every node so children can be linked regardless of
	// the order categories came back in.
	for _, category := range categories {
		nodes[category.ID] = model.NewCategoryNode(*category)
	}

	roots := make([]*model.CategoryNode, 0, len(categories))
	// Second pass: attach each node to its parent, or record it as a root.
	// Iterating the slice keeps the caller's ordering; nodes are visited in the
	// same order so children stay sorted the way GetAll returned them.
	for _, category := range categories {
		node := nodes[category.ID]
		if category.ParentID != nil {
			if parent, ok := nodes[*category.ParentID]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}
	return roots
}
