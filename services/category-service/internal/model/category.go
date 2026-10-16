// Package model holds the domain types exchanged with clients and the database.
package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Category is a node in the category hierarchy.
//
// Description and ParentID are nullable columns, so they are pointers and
// marshal to JSON null. The previous sql.NullString/sql.NullInt64 fields
// serialised as {"String":"","Valid":false}, which leaked the storage shape
// into the API and was not valid against the documented schema.
type Category struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	ParentID    *int64    `json:"parent_id"`
	TenantID    string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// IsGlobal reports whether the category belongs to the administrator-managed
// global catalogue rather than one merchant.
func (c Category) IsGlobal() bool { return c.TenantID == "" }

// CategoryNode is a category together with its children. It embeds Category so
// the two representations cannot drift apart.
type CategoryNode struct {
	Category
	Children []*CategoryNode `json:"children"`
}

// CreateCategoryRequest is the body of POST /api/v1/categories.
type CreateCategoryRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ParentID    *int64  `json:"parent_id"`
}

// UpdateCategoryRequest is the body of PUT /api/v1/categories/{id}.
//
// A plain pointer cannot express the difference between "field omitted" and
// "field set to null", and those differ for updates: the first leaves the
// column alone, the second clears it. UnmarshalJSON therefore records which
// keys were present, and the Has* accessors report that.
type UpdateCategoryRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ParentID    *int64  `json:"parent_id"`

	present map[string]bool
}

// updateFields is the set of keys an update body may contain.
var updateFields = map[string]bool{
	"name":        true,
	"description": true,
	"parent_id":   true,
}

// UnmarshalJSON decodes the body, rejecting unknown keys and remembering which
// ones were present even when their value is null.
func (r *UpdateCategoryRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	for key := range raw {
		if !updateFields[key] {
			// Checked here because a custom unmarshaller takes over decoding,
			// so the decoder's DisallowUnknownFields no longer applies.
			return fmt.Errorf("unknown field %q", key)
		}
	}

	r.present = make(map[string]bool, len(raw))
	for key, value := range raw {
		r.present[key] = true

		switch key {
		case "name":
			if isJSONNull(value) {
				continue
			}
			var name string
			if err := json.Unmarshal(value, &name); err != nil {
				return fmt.Errorf("name must be a string: %w", err)
			}
			r.Name = &name

		case "description":
			if isJSONNull(value) {
				continue
			}
			var description string
			if err := json.Unmarshal(value, &description); err != nil {
				return fmt.Errorf("description must be a string: %w", err)
			}
			r.Description = &description

		case "parent_id":
			if isJSONNull(value) {
				continue
			}
			var parentID int64
			if err := json.Unmarshal(value, &parentID); err != nil {
				return fmt.Errorf("parent_id must be an integer: %w", err)
			}
			r.ParentID = &parentID
		}
	}
	return nil
}

// HasName reports whether the request set the name field, even to null.
func (r *UpdateCategoryRequest) HasName() bool { return r.present["name"] }

// HasDescription reports whether the request set the description field.
func (r *UpdateCategoryRequest) HasDescription() bool { return r.present["description"] }

// HasParentID reports whether the request set the parent_id field. A present
// but null parent_id means "detach from the current parent".
func (r *UpdateCategoryRequest) HasParentID() bool { return r.present["parent_id"] }

// IsEmpty reports whether the body would change nothing.
func (r *UpdateCategoryRequest) IsEmpty() bool { return len(r.present) == 0 }

func isJSONNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// NewCategoryNode wraps a category with an initialised, non-nil child slice so
// the JSON output is always an array rather than null.
func NewCategoryNode(c Category) *CategoryNode {
	return &CategoryNode{
		Category: c,
		Children: []*CategoryNode{},
	}
}
