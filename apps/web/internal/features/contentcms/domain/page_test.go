package domain

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestBuiltinPresetsArePublishable(t *testing.T) {
	presets := BuiltinPresets()
	if len(presets) == 0 {
		t.Fatal("BuiltinPresets() returned no presets")
	}
	seen := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if _, ok := seen[preset.ID]; ok {
			t.Errorf("duplicate preset ID %q", preset.ID)
		}
		seen[preset.ID] = struct{}{}
		document := Document{SchemaVersion: SchemaVersion, Components: []Component{preset.Component}}
		if err := document.ValidateForPublish(); err != nil {
			t.Errorf("preset %q is not publishable: %v", preset.ID, err)
		}
	}
}

func TestDocumentValidateAcceptsNestedRegisteredComponents(t *testing.T) {
	document := Document{
		SchemaVersion: SchemaVersion,
		Components: []Component{{
			ID:   "section-1",
			Type: "container",
			Children: []Component{
				{ID: "heading-1", Type: "heading", Props: map[string]any{"text": "Welcome"}},
				{ID: "text-1", Type: "text", Props: map[string]any{"text": "Body copy"}},
				{ID: "button-1", Type: "button", Props: map[string]any{"label": "Read more", "href": "/articles/welcome"}},
				{ID: "image-1", Type: "image", Props: map[string]any{"src": "/assets/image.jpg", "alt": "Example"}},
				{ID: "spacer-1", Type: "spacer"},
			},
		}},
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestDocumentValidateRejectsUnsupportedVersionPropertiesAndTypes(t *testing.T) {
	tests := []struct {
		name     string
		document Document
	}{
		{name: "unsupported schema version", document: Document{SchemaVersion: SchemaVersion + 1}},
		{name: "unknown property", document: Document{SchemaVersion: SchemaVersion, Components: []Component{{ID: "heading-1", Type: "heading", Props: map[string]any{"html": "<script>"}}}}},
		{name: "unsupported component type", document: Document{SchemaVersion: SchemaVersion, Components: []Component{{ID: "video-1", Type: "video"}}}},
		{name: "non-container with children", document: Document{SchemaVersion: SchemaVersion, Components: []Component{{ID: "text-1", Type: "text", Children: []Component{{ID: "child-1", Type: "text"}}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.document.Validate(); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDocument", err)
			}
		})
	}
}

func TestDocumentValidateForPublishRejectsIncompleteComponents(t *testing.T) {
	document := Document{SchemaVersion: SchemaVersion, Components: []Component{{ID: "button-1", Type: "button", Props: map[string]any{"label": "", "href": ""}}}}
	if err := document.Validate(); err != nil {
		t.Fatalf("draft validation rejected incomplete component: %v", err)
	}
	if err := document.ValidateForPublish(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("publish validation = %v, want incomplete component rejection", err)
	}
}

func TestDocumentValidateRejectsUnsafeImageAndButtonURLs(t *testing.T) {
	for _, component := range []Component{
		{ID: "image-1", Type: "image", Props: map[string]any{"src": "javascript:alert(1)"}},
		{ID: "image-1", Type: "image", Props: map[string]any{"src": "//evil.example/image.png"}},
		{ID: "button-1", Type: "button", Props: map[string]any{"label": "go", "href": "http://example.test"}},
		{ID: "button-1", Type: "button", Props: map[string]any{"label": "go", "href": "https://user:pass@example.test"}},
	} {
		document := Document{SchemaVersion: SchemaVersion, Components: []Component{component}}
		if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) {
			t.Errorf("Validate(%#v) error = %v, want ErrInvalidDocument", component, err)
		}
	}
}

func TestDocumentValidateAcceptsDepthSixAndRejectsDepthSeven(t *testing.T) {
	if err := (Document{SchemaVersion: SchemaVersion, Components: []Component{componentChain(MaxComponentDepth)}}).Validate(); err != nil {
		t.Fatalf("depth %d rejected: %v", MaxComponentDepth, err)
	}
	err := (Document{SchemaVersion: SchemaVersion, Components: []Component{componentChain(MaxComponentDepth + 1)}}).Validate()
	if !errors.Is(err, ErrInvalidDocument) || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("depth %d error = %v, want depth validation error", MaxComponentDepth+1, err)
	}
}

func TestDocumentValidateEnforcesNodeLimit(t *testing.T) {
	components := make([]Component, MaxComponentNodes)
	for i := range components {
		components[i] = Component{ID: "node-" + strconv.Itoa(i), Type: "spacer"}
	}
	document := Document{SchemaVersion: SchemaVersion, Components: components}
	if err := document.Validate(); err != nil {
		t.Fatalf("%d nodes rejected: %v", MaxComponentNodes, err)
	}
	document.Components = append(document.Components, Component{ID: "node-extra", Type: "spacer"})
	if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) || !strings.Contains(err.Error(), "nodes") {
		t.Fatalf("%d nodes error = %v, want node limit error", len(document.Components), err)
	}
}

func TestDocumentValidateRejectsDuplicateIDsAcrossTree(t *testing.T) {
	document := Document{SchemaVersion: SchemaVersion, Components: []Component{{
		ID: "same", Type: "container", Children: []Component{{ID: "same", Type: "text"}},
	}}}
	if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Validate() error = %v, want duplicate ID error", err)
	}
}

func componentChain(depth int) Component {
	component := Component{ID: "node-" + strconv.Itoa(depth), Type: "spacer"}
	for i := depth - 1; i >= 1; i-- {
		component = Component{ID: "node-" + strconv.Itoa(i), Type: "container", Children: []Component{component}}
	}
	return component
}

func TestMigrateLegacySectionPreservesUnknownProps(t *testing.T) {
	t.Parallel()
	source := Document{
		SchemaVersion: LegacySchemaVersion,
		Components: []Component{{
			ID:   "legacy-section",
			Type: "section",
			Props: map[string]any{
				"layout":     "grid",
				"columns":    2,
				"background": "#abcdef",
				"mystery":    "kept",
			},
			Children: []Component{
				{ID: "legacy-heading", Type: "heading", Props: map[string]any{"text": "Legacy"}},
			},
		}},
	}

	migrated, err := Migrate(source)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if migrated.SchemaVersion != SchemaVersion {
		t.Fatalf("migrated schemaVersion = %d, want %d", migrated.SchemaVersion, SchemaVersion)
	}
	if len(migrated.Components) != 1 || migrated.Components[0].Type != "container" {
		t.Fatalf("migrated components = %#v", migrated.Components)
	}
	if migrated.Components[0].Extensions["legacyProps"] == nil {
		t.Fatalf("legacy props were not preserved: %#v", migrated.Components[0].Extensions)
	}
	if !strings.Contains(string(migrated.Components[0].Extensions["legacyProps"]), `"mystery":"kept"`) {
		t.Fatalf("legacy props payload = %s", migrated.Components[0].Extensions["legacyProps"])
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated.Validate() error = %v", err)
	}
}

func TestResolvePropsInheritance(t *testing.T) {
	t.Parallel()
	component := Component{
		ID:   "heading-1",
		Type: "heading",
		Props: map[string]any{
			"text":   "Base",
			"align":  "left",
			"size":   "lg",
			"weight": "normal",
			"color":  "",
		},
		Responsive: map[string]map[string]any{
			"tablet": {"align": "center", "size": "xl"},
			"mobile": {"align": "right"},
		},
	}

	if got := component.ResolveProps("desktop")["align"]; got != "left" {
		t.Fatalf("desktop align = %#v, want left", got)
	}
	if got := component.ResolveProps("tablet")["align"]; got != "center" {
		t.Fatalf("tablet align = %#v, want center", got)
	}
	if got := component.ResolveProps("tablet")["size"]; got != "xl" {
		t.Fatalf("tablet size = %#v, want xl", got)
	}
	if got := component.ResolveProps("mobile")["align"]; got != "right" {
		t.Fatalf("mobile align = %#v, want right", got)
	}
	if got := component.ResolveProps("mobile")["size"]; got != "xl" {
		t.Fatalf("mobile size = %#v, want tablet's xl", got)
	}
}
