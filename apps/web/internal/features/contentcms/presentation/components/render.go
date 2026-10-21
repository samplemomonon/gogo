// Package components is the single Go registry for approved page components.
// The editor's generated inspector, the draft preview, and public pages all
// read component definitions from domain and render through this package.
package components

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"gogo/internal/features/contentcms/domain"
)

// RenderContext carries only server-provided values. Components never resolve
// data from the client URL or from user-authored expressions.
type RenderContext struct {
	PageTitle     string
	Slug          string
	TenantID      string
	SiteName      string
	BindingValues map[string]string
}

func (c RenderContext) values() map[string]string {
	if c.BindingValues != nil {
		return c.BindingValues
	}
	return domain.BindingValuesWithSite(c.PageTitle, c.Slug, c.TenantID, c.SiteName)
}

type renderer func(domain.Component, RenderContext) templ.Component

// Registry maps every approved type to its renderer. A test asserts this map
// and domain.ComponentDefinitions stay in lockstep.
//
// It is populated in init because renderContainer itself calls
// RenderComponent; a map literal that referenced the renderers would be an
// initialization cycle in Go's package dependency analysis.
var Registry = map[string]renderer{}

func init() {
	Registry["container"] = renderContainer
	Registry["heading"] = renderHeading
	Registry["text"] = renderText
	Registry["image"] = renderImage
	Registry["button"] = renderButton
	Registry["spacer"] = renderSpacer
	Registry["navbar"] = renderNavbar
	Registry["form"] = renderForm
	Registry["data-text"] = renderDataText
	Registry["ad-slot"] = renderAdSlot
}

// RenderComponent returns a registered renderer, or a safe diagnostic for an
// unknown type. Public pages validate before rendering; draft previews use the
// diagnostic so one bad node does not take down the editor.
func RenderComponent(component domain.Component, context RenderContext) templ.Component {
	render, ok := Registry[component.Type]
	if !ok {
		return ComponentDiagnostic(component.Type)
	}
	return render(component, context)
}

// ComponentClass is the stable hook for generated responsive CSS and editor
// selection. IDs are validated by domain before they reach this function.
func ComponentClass(component domain.Component) string {
	return "cms-" + component.ID
}

// ComponentActionClass is the stable hook for a component's inner action.
// Buttons use it so Full width can be overridden per breakpoint without
// changing the action's static utility classes.
func ComponentActionClass(component domain.Component) string {
	return ComponentClass(component) + "-action"
}

func ComponentRootClass(component domain.Component, extra string) string {
	class := ComponentClass(component)
	if extra != "" {
		return class + " " + extra
	}
	return class
}

func safeHref(value string) templ.SafeURL {
	if domain.SafePublicURL(value) {
		return templ.SafeURL(value)
	}
	return templ.SafeURL("#")
}

func safeImage(value string) string {
	if domain.SafePublicURL(value) {
		return value
	}
	return ""
}

func bindingText(component domain.Component, context RenderContext) string {
	fallback, _ := component.Props["fallback"].(string)
	if component.Binding == nil {
		return fallback
	}
	value, ok := domain.ResolveBinding(component.Binding, context.values())
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func arrayObjects(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringValue(props map[string]any, key string) string {
	value, _ := props[key].(string)
	return value
}

func boolValue(props map[string]any, key string, fallback bool) bool {
	value, ok := props[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func formAction(component domain.Component, context RenderContext) string {
	if context.Slug == "" {
		return "#"
	}
	return "/pages/" + context.Slug + "/forms/" + component.ID
}

// adSlotRootClass keeps the reserved placement's published size aligned with
// the inspector's Format control. It is a placeholder, not an ad request: no
// third-party script or iframe is ever rendered from a page document.
func adSlotRootClass(component domain.Component) string {
	extra := "ad-slot flex flex-col items-center justify-center gap-1 rounded-xl border border-dashed border-base-300 bg-base-200/60 p-4 text-center"
	switch stringValue(component.Props, "format") {
	case "rectangle":
		extra += " ad-slot--rectangle"
	case "inline":
		extra += " ad-slot--inline"
	}
	return ComponentRootClass(component, extra)
}

// StyleSheet produces the published stylesheet for a document. It is generated
// only from fixed enum maps, validated colors, bounded numbers, and validated
// component IDs; no user-authored CSS can reach it.
func StyleSheet(components []domain.Component) string {
	var builder strings.Builder
	for _, component := range components {
		appendComponentStyles(&builder, component)
	}
	return builder.String()
}

// StyleElement wraps the generated stylesheet for a templ view. The only text
// inside is generated by StyleSheet, never by a page document.
func StyleElement(components []domain.Component) string {
	css := StyleSheet(components)
	if css == "" {
		return ""
	}
	return "<style>" + css + "</style>"
}

func appendComponentStyles(builder *strings.Builder, component domain.Component) {
	class := ComponentClass(component)
	baseStyle := styleDeclarations(component.Type, component.Props)
	base := baseStyle
	if component.Hidden {
		base = append(base, "display:none")
	}
	writeRule(builder, class, base)
	writeRule(builder, class+"-action", buttonActionDeclarations(component.Type, component.Props, false))

	if len(component.Responsive) > 0 {
		tabletProps := component.ResolveProps(domain.BreakpointTablet)
		tablet := styleDeclarations(component.Type, tabletProps)
		tablet = append(resetDeclarations(component.Type, baseStyle, tablet), tablet...)
		if component.Hidden {
			tablet = append(tablet, "display:none")
		}
		writeMediaRules(
			builder, "(min-width:768px) and (max-width:1023px)", class, tablet,
			buttonActionDeclarations(component.Type, tabletProps, true),
		)

		mobileProps := component.ResolveProps(domain.BreakpointMobile)
		mobile := styleDeclarations(component.Type, mobileProps)
		mobile = append(resetDeclarations(component.Type, baseStyle, mobile), mobile...)
		if component.Hidden {
			mobile = append(mobile, "display:none")
		}
		writeMediaRules(
			builder, "(max-width:767px)", class, mobile,
			buttonActionDeclarations(component.Type, mobileProps, true),
		)
	}
	for _, child := range component.Children {
		appendComponentStyles(builder, child)
	}
}

func writeRule(builder *strings.Builder, class string, declarations []string) {
	if len(declarations) == 0 {
		return
	}
	fmt.Fprintf(builder, ".%s{%s}", class, strings.Join(declarations, ";"))
}

func writeMediaRules(builder *strings.Builder, media, class string, declarations, action []string) {
	if len(declarations) == 0 && len(action) == 0 {
		return
	}
	fmt.Fprintf(builder, "@media %s{", media)
	writeRule(builder, class, declarations)
	writeRule(builder, class+"-action", action)
	builder.WriteString("}")
}

// buttonActionDeclarations keeps the action itself aligned with the
// responsive Full width setting. The wrapper can be full width while a
// breakpoint turns the button back into its natural width.
func buttonActionDeclarations(componentType string, props map[string]any, includeReset bool) []string {
	if componentType != "button" {
		return nil
	}
	if full, _ := props["fullWidth"].(bool); full {
		return []string{"width:100%"}
	}
	if includeReset {
		return []string{"width:auto"}
	}
	return nil
}

// resetDeclarations closes the cascade gap between the base rule and a
// breakpoint rule. When a responsive override removes a declaration (for
// example max-width:full, aspect:auto, sticky:false, or a blank color), the
// base declaration would otherwise continue to apply at that breakpoint.
func resetDeclarations(componentType string, base, resolved []string) []string {
	resolvedProperties := make(map[string]struct{}, len(resolved))
	for _, declaration := range resolved {
		property, _, ok := strings.Cut(declaration, ":")
		if ok {
			resolvedProperties[property] = struct{}{}
		}
	}
	resets := make([]string, 0, len(base))
	for _, declaration := range base {
		property, _, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		if _, stillSet := resolvedProperties[property]; stillSet {
			continue
		}
		value, reset := responsiveResetValue(componentType, property)
		if reset {
			resets = append(resets, property+":"+value)
		}
	}
	return resets
}

func responsiveResetValue(componentType, property string) (string, bool) {
	switch property {
	case "aspect-ratio":
		return "auto", true
	case "background-color":
		// These components carry a base-100 surface class in their markup.
		// Clearing the authored color must restore that surface, not expose
		// whatever happens to be behind it.
		switch componentType {
		case "container", "navbar", "form":
			return "var(--color-base-100)", true
		default:
			return "transparent", true
		}
	case "color":
		return "inherit", true
	case "margin-inline":
		return "0", true
	case "max-width":
		return "none", true
	case "position":
		return "static", true
	case "top":
		return "auto", true
	case "width":
		return "auto", true
	default:
		return "", false
	}
}

var colorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func styleDeclarations(componentType string, props map[string]any) []string {
	declarations := make([]string, 0, 8)
	add := func(property, value string) {
		if value != "" {
			declarations = append(declarations, property+":"+value)
		}
	}
	spacing := func(key string) string {
		token, _ := props[key].(string)
		pixels, _ := domain.SpacingPixels(token)
		return pixels
	}
	color := func(key string) string {
		value, _ := props[key].(string)
		if value == "" || !colorPattern.MatchString(value) {
			return ""
		}
		return value
	}
	alignment := func() string {
		switch value, _ := props["align"].(string); value {
		case "left", "center", "right":
			return value
		}
		return ""
	}
	typography := func() {
		switch value, _ := props["size"].(string); value {
		case "sm":
			add("font-size", "0.875rem")
		case "md":
			add("font-size", "1rem")
		case "lg":
			add("font-size", "1.125rem")
		case "xl":
			add("font-size", "1.5rem")
		case "2xl":
			add("font-size", "2.25rem")
		}
		switch value, _ := props["weight"].(string); value {
		case "normal":
			add("font-weight", "400")
		case "medium":
			add("font-weight", "500")
		case "semibold":
			add("font-weight", "600")
		case "bold":
			add("font-weight", "700")
		}
		if align := alignment(); align != "" {
			add("text-align", align)
		}
		add("color", color("color"))
	}

	switch componentType {
	case "container":
		switch layout, _ := props["layout"].(string); layout {
		case "grid":
			add("display", "grid")
			columns := 2
			if number, ok := numeric(props["columns"]); ok && number >= 1 && number <= 4 {
				columns = int(number)
			}
			add("grid-template-columns", fmt.Sprintf("repeat(%d,minmax(0,1fr))", columns))
		case "stack":
			add("display", "flex")
			add("flex-direction", "column")
		}
		add("gap", spacing("gap"))
		add("padding", spacing("padding"))
		add("background-color", color("background"))
		switch maxWidth, _ := props["maxWidth"].(string); maxWidth {
		case "wide":
			add("max-width", "72rem")
			add("margin-inline", "auto")
		case "narrow":
			add("max-width", "48rem")
			add("margin-inline", "auto")
		}
		if rounded, ok := props["rounded"].(bool); ok {
			if rounded {
				add("border-radius", "1rem")
			} else {
				add("border-radius", "0")
			}
		}
	case "heading", "text", "data-text":
		typography()
	case "image":
		switch aspect, _ := props["aspect"].(string); aspect {
		case "square":
			add("aspect-ratio", "1 / 1")
			add("object-fit", "cover")
		case "video":
			add("aspect-ratio", "16 / 9")
			add("object-fit", "cover")
		}
		switch fit, _ := props["fit"].(string); fit {
		case "cover", "contain":
			add("object-fit", fit)
		}
		switch width, _ := props["width"].(string); width {
		case "wide":
			add("max-width", "72rem")
		case "narrow":
			add("max-width", "48rem")
		}
		if rounded, ok := props["rounded"].(bool); ok {
			if rounded {
				add("border-radius", "0.75rem")
			} else {
				add("border-radius", "0")
			}
		}
	case "button":
		// The wrapper is the flex container around the action, so alignment is
		// a justify-content value on the wrapper rather than a margin on the
		// inline-flex anchor. The action class carries the full-width override
		// so it can also be cleared at a responsive breakpoint.
		switch alignment() {
		case "center":
			add("justify-content", "center")
		case "right":
			add("justify-content", "flex-end")
		case "left":
			add("justify-content", "flex-start")
		}
		if full, ok := props["fullWidth"].(bool); ok && full {
			add("width", "100%")
		}
	case "spacer":
		add("height", spacing("height"))
	case "navbar":
		if sticky, _ := props["sticky"].(bool); sticky {
			add("position", "sticky")
			add("top", "0")
		}
		add("background-color", color("background"))
		add("color", color("color"))
	case "form":
		if align := alignment(); align != "" {
			add("text-align", align)
		}
		add("background-color", color("background"))
		add("color", color("color"))
	case "ad-slot":
		// The placeholder carries its own sizing classes. The slot ID is a
		// data attribute, not CSS, so nothing user-authored reaches this rule.
	}
	return declarations
}

func numeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
