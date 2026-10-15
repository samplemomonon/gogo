// Package repository implements PostgreSQL persistence for categories. All SQL
// lives here; higher layers work with domain types and sentinel errors.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"category-service/internal/apperror"
	"category-service/internal/model"
)

// CategoryRepository stores categories in PostgreSQL.
type CategoryRepository struct {
	db *sql.DB
}

// NewCategoryRepository returns a repository backed by db.
func NewCategoryRepository(db *sql.DB) *CategoryRepository {
	return &CategoryRepository{db: db}
}

const categoryColumns = `id, name, description, parent_id, created_at, updated_at`

// scannedCategory holds a row using the sql null types, which the driver needs
// for scanning but which the model does not expose. Converting through this
// type keeps the nullable columns out of the domain model.
type scannedCategory struct {
	id          int64
	name        string
	description sql.NullString
	parentID    sql.NullInt64
	createdAt   time.Time
	updatedAt   time.Time
}

func (s scannedCategory) category() *model.Category {
	c := &model.Category{
		ID:        s.id,
		Name:      s.name,
		CreatedAt: s.createdAt,
		UpdatedAt: s.updatedAt,
	}
	if s.description.Valid {
		description := s.description.String
		c.Description = &description
	}
	if s.parentID.Valid {
		parentID := s.parentID.Int64
		c.ParentID = &parentID
	}
	return c
}

// Create inserts a new category and returns the stored row.
func (r *CategoryRepository) Create(ctx context.Context, req *model.CreateCategoryRequest) (*model.Category, error) {
	const query = `
		INSERT INTO categories (name, description, parent_id)
		VALUES ($1, $2, $3)
		RETURNING ` + categoryColumns

	category := &model.Category{}
	err := r.db.QueryRowContext(ctx, query, req.Name, req.Description, req.ParentID).
		Scan(
			&category.ID,
			&category.Name,
			&category.Description,
			&category.ParentID,
			&category.CreatedAt,
			&category.UpdatedAt,
		)
	if err != nil {
		return nil, fmt.Errorf("create category: %w", err)
	}
	return category, nil
}

// GetByID returns a single category.
func (r *CategoryRepository) GetByID(ctx context.Context, id int64) (*model.Category, error) {
	const query = `SELECT ` + categoryColumns + ` FROM categories WHERE id = $1`

	var row scannedCategory
	if err := r.db.QueryRowContext(ctx, query, id).Scan(
		&row.id,
		&row.name,
		&row.description,
		&row.parentID,
		&row.createdAt,
		&row.updatedAt,
	); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("%w: category %d", apperror.ErrNotFound, id)
		default:
			return nil, fmt.Errorf("get category %d: %w", id, err)
		}
	}
	return row.category(), nil
}

// GetAll returns every category ordered by name.
func (r *CategoryRepository) GetAll(ctx context.Context) ([]*model.Category, error) {
	const query = `SELECT ` + categoryColumns + ` FROM categories ORDER BY name ASC`
	return r.query(ctx, query, "list categories")
}

// GetChildren returns the direct children of parentID ordered by name.
func (r *CategoryRepository) GetChildren(ctx context.Context, parentID int64) ([]*model.Category, error) {
	const query = `SELECT ` + categoryColumns + `
		FROM categories WHERE parent_id = $1 ORDER BY name ASC`
	return r.query(ctx, query, "list children of category %d", parentID)
}

// GetAncestors returns the chain from the immediate parent up to the root.
// Passing the category's own id excludes the category itself.
func (r *CategoryRepository) GetAncestors(ctx context.Context, id int64) ([]*model.Category, error) {
	// The recursive term walks parent_id upward. depth counts upward hops so
	// ordering by depth DESC yields immediate-parent-first.
	const query = `
		WITH RECURSIVE ancestors AS (
			SELECT id, name, description, parent_id, created_at, updated_at, 0 AS depth
			FROM categories
			WHERE id = $1

			UNION ALL

			SELECT c.id, c.name, c.description, c.parent_id, c.created_at, c.updated_at, a.depth + 1
			FROM categories c
			JOIN ancestors a ON c.id = a.parent_id
		)
		SELECT id, name, description, parent_id, created_at, updated_at
		FROM ancestors
		WHERE depth > 0
		ORDER BY depth ASC`
	return r.query(ctx, query, "list ancestors of category %d", id)
}

// query runs a multi-row SELECT and scans every row.
func (r *CategoryRepository) query(ctx context.Context, query, op string, args ...any) ([]*model.Category, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fmt.Sprintf(op, args...), err)
	}
	defer func() { _ = rows.Close() }()

	// An empty slice, not nil, so the API renders [] rather than null.
	categories := make([]*model.Category, 0)
	for rows.Next() {
		var row scannedCategory
		if err := rows.Scan(
			&row.id,
			&row.name,
			&row.description,
			&row.parentID,
			&row.createdAt,
			&row.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("%s: scan row: %w", fmt.Sprintf(op, args...), err)
		}
		categories = append(categories, row.category())
	}
	// Without this check a mid-iteration network failure looks like a short result.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", fmt.Sprintf(op, args...), err)
	}
	return categories, nil
}

// Update applies the non-nil fields of req to the category with the given id.
// Calling it with an empty req is a no-op that returns the current row.
func (r *CategoryRepository) Update(
	ctx context.Context,
	id int64,
	req *model.UpdateCategoryRequest,
) (*model.Category, error) {
	// Build SET clauses only from the keys the caller actually sent, so a
	// partial update does not clobber the columns it left out. A present key
	// with a nil value writes NULL, which is how a field is cleared.
	var (
		clauses []string
		args    []any
	)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", clause, len(args)))
	}

	if req.HasName() {
		add("name", req.Name)
	}
	if req.HasDescription() {
		add("description", req.Description)
	}
	if req.HasParentID() {
		add("parent_id", req.ParentID)
	}

	if len(clauses) == 0 {
		// Nothing to change: return the row as-is instead of emitting invalid SQL.
		return r.GetByID(ctx, id)
	}

	args = append(args, id)
	query := fmt.Sprintf(
		`UPDATE categories SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(clauses, ", "), len(args), categoryColumns,
	)

	var row scannedCategory
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&row.id,
		&row.name,
		&row.description,
		&row.parentID,
		&row.createdAt,
		&row.updatedAt,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: category %d", apperror.ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("update category %d: %w", id, err)
	}
	return row.category(), nil
}

// Delete removes a category. The schema cascades to descendants.
func (r *CategoryRepository) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM categories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete category %d: %w", id, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete category %d: rows affected: %w", id, err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("%w: category %d", apperror.ErrNotFound, id)
	}
	return nil
}

// WouldCreateCircularReference reports whether making newParentID the parent of
// categoryID would close a loop, i.e. whether newParentID is categoryID itself
// or one of its descendants.
func (r *CategoryRepository) WouldCreateCircularReference(
	ctx context.Context,
	categoryID, newParentID int64,
) (bool, error) {
	// The recursive term descends from categoryID; if newParentID shows up in
	// that subtree then the new edge points back into it.
	const query = `
		WITH RECURSIVE descendants AS (
			SELECT id, parent_id FROM categories WHERE id = $1

			UNION ALL

			SELECT c.id, c.parent_id
			FROM categories c
			JOIN descendants d ON c.parent_id = d.id
		)
		SELECT EXISTS (SELECT 1 FROM descendants WHERE id = $2)`

	var exists bool
	if err := r.db.QueryRowContext(ctx, query, categoryID, newParentID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check circular reference for category %d: %w", categoryID, err)
	}
	return exists, nil
}

// CountDescendants returns how many categories would be removed alongside the
// given category, so callers can warn before a cascading delete.
func (r *CategoryRepository) CountDescendants(ctx context.Context, id int64) (int, error) {
	const query = `
		WITH RECURSIVE descendants AS (
			SELECT id, parent_id FROM categories WHERE id = $1

			UNION ALL

			SELECT c.id, c.parent_id
			FROM categories c
			JOIN descendants d ON c.parent_id = d.id
		)
		SELECT COUNT(*) FROM descendants WHERE id <> $1`

	var count int
	if err := r.db.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("count descendants of category %d: %w", id, err)
	}
	return count, nil
}
