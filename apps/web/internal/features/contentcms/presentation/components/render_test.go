package components_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gogo/internal/features/contentcms/domain"
	"gogo/internal/features/contentcms/presentation/components"
)

func render(t *testing.T, component domain.Component) string {
	t.Helper()
	var output bytes.Buffer
	if err := components.RenderComponent(component, components.RenderContext{
		PageTitle: "Bound page",
		Slug:      "bound",
		TenantID:  "default",
		BindingValues: map[string]string{
			"page.title": "Bound page",
			"page.slug":  "bound",
		},
	}).Render(context.Background(), &output); err != nil {
		t.Fatalf("render component: %v", err)
	}
	return output.String()
}

func TestRegistryCoversEveryDefinition(t *testing.T) {
	t.Parallel()
	definitions := map[string]struct{}{}
	for _, definition := range domain.ComponentDefinitions {
		definitions[definition.Type] = struct{}{}
	}
	for componentType := range definitions {
		if _, ok := components.Registry[componentType]; !ok {
			t.Errorf("component definition %q has no renderer", componentType)
		}
	}
	for componentType := range components.Registry {
		if _, ok := definitions[componentType]; !ok {
			t.Errorf("renderer %q has no component definition", componentType)
		}
	}
}

func TestRequiredStarterComponentsRenderInAContainer(t *testing.T) {
	t.Parallel()
	output := render(t, domain.Component{
		ID: "container-1", Type: "container",
		Props: map[string]any{"layout": "stack", "gap": "md", "padding": "md", "maxWidth": "full", "rounded": true},
		Children: []domain.Component{
			{ID: "heading-1", Type: "heading", Props: map[string]any{"text": "A heading", "level": "h2"}},
			{ID: "text-1", Type: "text", Props: map[string]any{"text": "A paragraph"}},
			{ID: "image-1", Type: "image", Props: map[string]any{"src": "/assets/img/placeholder.svg", "alt": "A description", "aspect": "video", "width": "full", "fit": "cover", "rounded": true}},
			{ID: "button-1", Type: "button", Props: map[string]any{"label": "Continue", "href": "/next", "variant": "primary", "size": "md", "align": "left", "fullWidth": false}},
			{ID: "ad-1", Type: "ad-slot", Props: map[string]any{"slot": "content", "label": "Advertisement", "format": "inline", "active": true}},
		},
	})
	for _, expected := range []string{
		`data-cms-component="container"`,
		`<h2`, `A heading`,
		`<p`, `A paragraph`,
		`<img`, `src="/assets/img/placeholder.svg"`, `alt="A description"`,
		`<a`, `href="/next"`, `Continue`, `cms-button-1-action`,
		`data-ad-slot="content"`, `Advertisement`,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("starter component output missing %q: %s", expected, output)
		}
	}
}

func TestHeadingEscapesText(t *testing.T) {
	t.Parallel()
	output := render(t, domain.Component{
		ID: "heading-1", Type: "heading",
		Props: map[string]any{"text": "<script>alert(1)</script>", "level": "h2"},
	})
	if strings.Contains(output, "<script>") || !strings.Contains(output, "&lt;script&gt;") {
		t.Fatalf("heading text was not escaped: %s", output)
	}
}

func TestDataBindingUsesOnlyServerValues(t *testing.T) {
	t.Parallel()
	output := render(t, domain.Component{
		ID: "bound-1", Type: "data-text",
		Binding: &domain.DataBinding{Source: "page", Field: "title"},
		Props:   map[string]any{"fallback": "Fallback", "align": "left", "size": "lg", "weight": "normal", "color": ""},
	})
	if !strings.Contains(output, "Bound page") || strings.Contains(output, "Fallback") {
		t.Fatalf("bound text did not resolve the server value: %s", output)
	}
}

func TestFormRendersRealSameOriginActionAndFields(t *testing.T) {
	t.Parallel()
	output := render(t, domain.Component{
		ID: "form-1", Type: "form",
		Props: map[string]any{
			"title": "Contact", "submitLabel": "Send",
			"fields": []any{
				map[string]any{"name": "email", "label": "Email", "type": "email", "required": true},
			},
			"align": "left", "background": "", "color": "",
		},
	})
	for _, expected := range []string{
		`action="/pages/bound/forms/form-1"`,
		`method="post"`,
		`name="email"`,
		`type="email"`,
		`required`,
		"Send",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("form output missing %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "disabled") {
		t.Fatalf("public form fields must not be disabled: %s", output)
	}
}

func TestAdSlotNeverEmitsScriptMarkup(t *testing.T) {
	t.Parallel()
	output := render(t, domain.Component{
		ID: "ad-1", Type: "ad-slot",
		Props: map[string]any{"slot": "home", "label": "Advertisement", "format": "leaderboard", "active": true},
	})
	if strings.Contains(output, "<script") || !strings.Contains(output, `data-ad-slot="home"`) {
		t.Fatalf("ad slot output is not script-free: %s", output)
	}
}

func TestAdSlotFormatControlsPublishedHeight(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		format string
		class  string
	}{
		{format: "leaderboard", class: "ad-slot"},
		{format: "rectangle", class: "ad-slot--rectangle"},
		{format: "inline", class: "ad-slot--inline"},
	} {
		output := render(t, domain.Component{
			ID: "ad-1", Type: "ad-slot",
			Props: map[string]any{"slot": "home", "format": test.format, "active": true},
		})
		if !strings.Contains(output, test.class) {
			t.Errorf("format %q output missing %q: %s", test.format, test.class, output)
		}
	}

	inactive := render(t, domain.Component{
		ID: "ad-2", Type: "ad-slot",
		Props: map[string]any{"slot": "home", "format": "inline", "active": false},
	})
	if strings.Contains(inactive, "<aside") {
		t.Fatalf("inactive ad slot should not render a placement: %s", inactive)
	}
}

func TestStyleSheetIncludesResponsiveOverrides(t *testing.T) {
	t.Parallel()
	css := components.StyleSheet([]domain.Component{{
		ID: "heading-1", Type: "heading",
		Props: map[string]any{"text": "Responsive", "align": "left", "size": "lg", "weight": "normal", "color": ""},
		Responsive: map[string]map[string]any{
			"tablet": {"align": "center"},
			"mobile": {"align": "right"},
		},
	}})
	for _, expected := range []string{
		".cms-heading-1{",
		"@media (min-width:768px) and (max-width:1023px)",
		"@media (max-width:767px)",
		"text-align:center",
		"text-align:right",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("stylesheet missing %q: %s", expected, css)
		}
	}
}

func TestStyleSheetReadsJSONNumberProperties(t *testing.T) {
	t.Parallel()
	css := components.StyleSheet([]domain.Component{{
		ID: "grid-1", Type: "container",
		Props: map[string]any{"layout": "grid", "columns": json.Number("3"), "gap": "none", "padding": "none"},
	}})
	if !strings.Contains(css, "grid-template-columns:repeat(3,minmax(0,1fr))") {
		t.Fatalf("stylesheet ignored json.Number columns: %s", css)
	}
}

func TestStyleSheetButtonAlignmentAndFullWidthArePublished(t *testing.T) {
	t.Parallel()
	button := domain.Component{
		ID: "button-1", Type: "button",
		Props: map[string]any{
			"label": "Continue", "href": "/next", "variant": "primary", "size": "md",
			"align": "center", "fullWidth": true,
		},
		Responsive: map[string]map[string]any{
			"tablet": {"align": "left", "fullWidth": false},
		},
	}
	output := render(t, button)
	if !strings.Contains(output, "cms-button-1-action") {
		t.Fatalf("button action class missing: %s", output)
	}
	if strings.Contains(output, "w-full") {
		t.Fatalf("button width must come from responsive generated CSS: %s", output)
	}

	css := components.StyleSheet([]domain.Component{button})
	for _, expected := range []string{
		".cms-button-1{justify-content:center;width:100%}",
		".cms-button-1-action{width:100%}",
		"justify-content:flex-start",
		"width:auto",
		".cms-button-1-action{width:auto}",
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("button stylesheet missing %q: %s", expected, css)
		}
	}
}

func TestStyleSheetResetsDeclarationsRemovedByAResponsiveOverride(t *testing.T) {
	t.Parallel()
	css := components.StyleSheet([]domain.Component{
		{
			ID: "container-1", Type: "container",
			Props: map[string]any{"layout": "stack", "maxWidth": "wide", "background": "#ffffff"},
			Responsive: map[string]map[string]any{
				"tablet": {"maxWidth": "full", "background": ""},
			},
		},
		{
			ID: "image-1", Type: "image",
			Props: map[string]any{"src": "/image.png", "alt": "Image", "aspect": "square", "width": "narrow", "fit": "cover", "rounded": true},
			Responsive: map[string]map[string]any{
				"mobile": {"aspect": "auto", "width": "full"},
			},
		},
		{
			ID: "navbar-1", Type: "navbar",
			Props: map[string]any{"brand": "Gogo", "links": []any{}, "sticky": true},
			Responsive: map[string]map[string]any{
				"tablet": {"sticky": false},
			},
		},
	})
	for _, expected := range []string{
		"max-width:none",
		"margin-inline:0",
		"background-color:var(--color-base-100)",
		"aspect-ratio:auto",
		"position:static",
		"top:auto",
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("responsive reset stylesheet missing %q: %s", expected, css)
		}
	}
}
