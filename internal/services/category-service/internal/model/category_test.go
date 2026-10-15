package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCategoryMarshalsNullsAsNull(t *testing.T) {
	t.Parallel()

	// A root category has no parent and may have no description. The old
	// sql.NullInt64 field serialised as {"Int64":0,"Valid":false}.
	encoded, err := json.Marshal(Category{ID: 1, Name: "Electronics"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, field := range []string{`"parent_id":null`, `"description":null`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("missing %s in %s", field, encoded)
		}
	}
	for _, leak := range []string{"Valid", "Int64", "String"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("storage type leaked into JSON (%s): %s", leak, encoded)
		}
	}
}

func TestCategoryMarshalsParentID(t *testing.T) {
	t.Parallel()

	parentID := int64(7)
	encoded, err := json.Marshal(Category{ID: 8, ParentID: &parentID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"parent_id":7`) {
		t.Errorf("want parent_id 7, got %s", encoded)
	}
}

func TestCategoryNodeChildrenIsAlwaysAnArray(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(NewCategoryNode(Category{ID: 1, Name: "leaf"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"children":[]`) {
		t.Errorf("want an empty children array, got %s", encoded)
	}
}

func TestUpdateRequestTracksPresence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        string
		wantPresent [3]bool
		wantNil     [3]bool
	}{
		{
			name:        "empty body",
			body:        `{}`,
			wantPresent: [3]bool{false, false, false},
			wantNil:     [3]bool{true, true, true},
		},
		{
			name:        "all fields set",
			body:        `{"name":"a","description":"b","parent_id":3}`,
			wantPresent: [3]bool{true, true, true},
			wantNil:     [3]bool{false, false, false},
		},
		{
			name:        "explicit nulls are present but nil",
			body:        `{"name":null,"description":null,"parent_id":null}`,
			wantPresent: [3]bool{true, true, true},
			wantNil:     [3]bool{true, true, true},
		},
		{
			name:        "mixed",
			body:        `{"description":"b","parent_id":null}`,
			wantPresent: [3]bool{false, true, true},
			wantNil:     [3]bool{true, false, true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var req UpdateCategoryRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.body, err)
			}

			gotPresent := [3]bool{req.HasName(), req.HasDescription(), req.HasParentID()}
			if gotPresent != tc.wantPresent {
				t.Errorf("presence = %v, want %v", gotPresent, tc.wantPresent)
			}

			gotNil := [3]bool{req.Name == nil, req.Description == nil, req.ParentID == nil}
			if gotNil != tc.wantNil {
				t.Errorf("nil-ness = %v, want %v", gotNil, tc.wantNil)
			}

			if wantEmpty := tc.body == `{}`; req.IsEmpty() != wantEmpty {
				t.Errorf("IsEmpty() = %v, want %v", req.IsEmpty(), wantEmpty)
			}
		})
	}
}

func TestUpdateRequestRejectsUnknownField(t *testing.T) {
	t.Parallel()

	// A custom unmarshaller disables DisallowUnknownFields, so the check moves
	// into UnmarshalJSON; this guards against regressing that.
	var req UpdateCategoryRequest
	if err := json.Unmarshal([]byte(`{"nope":1}`), &req); err == nil {
		t.Fatal("want an error for an unknown field, got nil")
	}
}

func TestUpdateRequestRejectsWrongType(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"name":1}`, `{"parent_id":"x"}`, `{"description":[]}`} {
		var req UpdateCategoryRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Errorf("%s: want an error, got nil", body)
		}
	}
}

func TestUpdateRequestDecodesValues(t *testing.T) {
	t.Parallel()

	var req UpdateCategoryRequest
	if err := json.Unmarshal([]byte(`{"name":"Books","parent_id":12}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Name == nil || *req.Name != "Books" {
		t.Errorf("Name = %v, want Books", req.Name)
	}
	if req.ParentID == nil || *req.ParentID != 12 {
		t.Errorf("ParentID = %v, want 12", req.ParentID)
	}
}
