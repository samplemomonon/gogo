package domain

// PropertyKind is the control and validation contract for one inspector
// field. The editor renders its form controls from these values; the server
// validates the same values before anything is persisted.
type PropertyKind string

const (
	PropertyString        PropertyKind = "string"
	PropertyText          PropertyKind = "text"
	PropertyNumber        PropertyKind = "number"
	PropertyBoolean       PropertyKind = "boolean"
	PropertyEnum          PropertyKind = "enum"
	PropertyColor         PropertyKind = "color"
	PropertyImage         PropertyKind = "image"
	PropertyURL           PropertyKind = "url"
	PropertySameOriginURL PropertyKind = "same-origin-url"
	PropertySpacing       PropertyKind = "spacing"
	PropertyArray         PropertyKind = "array"
	PropertyDataReference PropertyKind = "data-reference"
)

const (
	BreakpointDesktop = "desktop"
	BreakpointTablet  = "tablet"
	BreakpointMobile  = "mobile"
)

const (
	CategoryLayout      = "layout"
	CategoryContent     = "content"
	CategoryNavigation  = "navigation"
	CategoryForms       = "forms"
	CategoryDynamicData = "dynamic-data"
	CategoryAdvertising = "advertising"
)

// Category is the library grouping shown in the editor. The order is part of
// the editor contract.
type Category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var Categories = []Category{
	{ID: CategoryLayout, Label: "Layout"},
	{ID: CategoryContent, Label: "Content"},
	{ID: CategoryNavigation, Label: "Navigation"},
	{ID: CategoryForms, Label: "Forms"},
	{ID: CategoryDynamicData, Label: "Dynamic Data"},
	{ID: CategoryAdvertising, Label: "Advertising"},
}

// Option is one selectable value in an enum property.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Property is the complete metadata for one component property.
type Property struct {
	Key            string       `json:"key"`
	Label          string       `json:"label"`
	Kind           PropertyKind `json:"kind"`
	Group          string       `json:"group,omitempty"`
	Help           string       `json:"help,omitempty"`
	Placeholder    string       `json:"placeholder,omitempty"`
	Default        any          `json:"default,omitempty"`
	Options        []Option     `json:"options,omitempty"`
	Required       bool         `json:"required,omitempty"`
	Responsive     bool         `json:"responsive,omitempty"`
	Min            float64      `json:"min,omitempty"`
	Max            float64      `json:"max,omitempty"`
	HasRange       bool         `json:"hasRange,omitempty"`
	MaxLength      int          `json:"maxLength,omitempty"`
	MinItems       int          `json:"minItems,omitempty"`
	MaxItems       int          `json:"maxItems,omitempty"`
	ItemProperties []Property   `json:"itemProperties,omitempty"`
}

// ComponentDefinition is the single source of truth for what the editor may
// offer and what the server will persist or render.
type ComponentDefinition struct {
	Type            string     `json:"type"`
	Label           string     `json:"label"`
	Description     string     `json:"description"`
	Category        string     `json:"category"`
	Icon            string     `json:"icon"`
	AllowChildren   bool       `json:"allowChildren"`
	AllowedChildren []string   `json:"allowedChildren,omitempty"`
	BindingAllowed  bool       `json:"bindingAllowed,omitempty"`
	BindingRequired bool       `json:"bindingRequired,omitempty"`
	Properties      []Property `json:"properties"`
}

func (d ComponentDefinition) allowsChild(childType string) bool {
	if !d.AllowChildren {
		return false
	}
	if len(d.AllowedChildren) == 0 {
		return true
	}
	for _, allowed := range d.AllowedChildren {
		if allowed == childType {
			return true
		}
	}
	return false
}

var spacingOptions = []Option{
	{Value: "none", Label: "None"},
	{Value: "xs", Label: "Extra small"},
	{Value: "sm", Label: "Small"},
	{Value: "md", Label: "Medium"},
	{Value: "lg", Label: "Large"},
	{Value: "xl", Label: "Extra large"},
}

var spacingPixels = map[string]string{
	"none": "0",
	"xs":   "4px",
	"sm":   "8px",
	"md":   "16px",
	"lg":   "24px",
	"xl":   "40px",
}

func enumProperty(key, label, group string, options []Option, defaultValue string, responsive bool) Property {
	return Property{
		Key:        key,
		Label:      label,
		Kind:       PropertyEnum,
		Group:      group,
		Options:    options,
		Default:    defaultValue,
		Responsive: responsive,
	}
}

func stringProperty(key, label, group string, required bool, maxLength int) Property {
	return Property{Key: key, Label: label, Kind: PropertyString, Group: group, Required: required, MaxLength: maxLength}
}

func textProperty(key, label, group string, required bool, maxLength int) Property {
	return Property{Key: key, Label: label, Kind: PropertyText, Group: group, Required: required, MaxLength: maxLength}
}

func spacingProperty(key, label, group string, defaultValue string) Property {
	return Property{
		Key: key, Label: label, Kind: PropertySpacing, Group: group,
		Options: spacingOptions, Default: defaultValue, Responsive: true,
	}
}

func booleanProperty(key, label, group string, defaultValue bool) Property {
	return Property{Key: key, Label: label, Kind: PropertyBoolean, Group: group, Default: defaultValue, Responsive: true}
}

func colorProperty(key, label, group string) Property {
	return Property{Key: key, Label: label, Kind: PropertyColor, Group: group, Responsive: true}
}

var textAlignment = []Option{
	{Value: "left", Label: "Left"},
	{Value: "center", Label: "Center"},
	{Value: "right", Label: "Right"},
}

var textSizes = []Option{
	{Value: "sm", Label: "Small"},
	{Value: "md", Label: "Medium"},
	{Value: "lg", Label: "Large"},
	{Value: "xl", Label: "Extra large"},
	{Value: "2xl", Label: "Display"},
}

var fontWeights = []Option{
	{Value: "normal", Label: "Regular"},
	{Value: "medium", Label: "Medium"},
	{Value: "semibold", Label: "Semibold"},
	{Value: "bold", Label: "Bold"},
}

func alignmentProperties() []Property {
	return []Property{
		enumProperty("align", "Alignment", "Layout", textAlignment, "left", true),
		enumProperty("size", "Text size", "Typography", textSizes, "md", true),
		enumProperty("weight", "Font weight", "Typography", fontWeights, "normal", true),
		colorProperty("color", "Text color", "Typography"),
	}
}

// ComponentDefinitions is the closed component registry. Adding a type here
// does not make it render: components.Registry must have a renderer for every
// definition, and a test enforces that pairing.
var ComponentDefinitions = []ComponentDefinition{
	{
		Type: "container", Label: "Container", Category: CategoryLayout, Icon: "container",
		Description:   "A layout region that holds other components.",
		AllowChildren: true,
		Properties: []Property{
			enumProperty("layout", "Layout", "Layout", []Option{{Value: "stack", Label: "Stack"}, {Value: "grid", Label: "Grid"}}, "stack", true),
			{Key: "columns", Label: "Columns", Kind: PropertyNumber, Group: "Layout", Default: 2, Min: 1, Max: 4, HasRange: true, Responsive: true, Help: "Used when the layout is grid."},
			spacingProperty("gap", "Gap", "Layout", "md"),
			spacingProperty("padding", "Padding", "Layout", "md"),
			colorProperty("background", "Background", "Style"),
			enumProperty("maxWidth", "Maximum width", "Layout", []Option{{Value: "full", Label: "Full width"}, {Value: "wide", Label: "Wide"}, {Value: "narrow", Label: "Narrow"}}, "full", true),
			booleanProperty("rounded", "Rounded corners", "Style", true),
		},
	},
	{
		Type: "heading", Label: "Heading", Category: CategoryContent, Icon: "heading",
		Description: "A section heading with an explicit semantic level.",
		Properties: append([]Property{
			textProperty("text", "Heading text", "Content", true, 500),
			enumProperty("level", "HTML level", "Content", []Option{{Value: "h1", Label: "H1"}, {Value: "h2", Label: "H2"}, {Value: "h3", Label: "H3"}, {Value: "h4", Label: "H4"}}, "h2", false),
		}, alignmentProperties()...),
	},
	{
		Type: "text", Label: "Text", Category: CategoryContent, Icon: "text",
		Description: "A paragraph of escaped, read-only text.",
		Properties: append([]Property{
			textProperty("text", "Text", "Content", true, 20_000),
		}, alignmentProperties()...),
	},
	{
		Type: "image", Label: "Image", Category: CategoryContent, Icon: "image",
		Description: "An image from a safe HTTPS or same-origin URL.",
		Properties: []Property{
			{Key: "src", Label: "Image URL", Kind: PropertyImage, Group: "Content", Required: true, MaxLength: 2_000, Placeholder: "/assets/image.webp or https://…"},
			stringProperty("alt", "Alternative text", "Content", true, 500),
			enumProperty("aspect", "Aspect ratio", "Layout", []Option{{Value: "auto", Label: "Natural"}, {Value: "square", Label: "Square"}, {Value: "video", Label: "16:9"}}, "auto", true),
			enumProperty("fit", "Crop fit", "Layout", []Option{{Value: "cover", Label: "Cover"}, {Value: "contain", Label: "Contain"}}, "cover", true),
			enumProperty("width", "Width", "Layout", []Option{{Value: "full", Label: "Full width"}, {Value: "wide", Label: "Wide"}, {Value: "narrow", Label: "Narrow"}}, "full", true),
			booleanProperty("rounded", "Rounded corners", "Style", true),
		},
	},
	{
		Type: "button", Label: "Button", Category: CategoryContent, Icon: "button",
		Description: "A link styled as an action.",
		Properties: []Property{
			stringProperty("label", "Label", "Content", true, 200),
			{Key: "href", Label: "Destination", Kind: PropertyURL, Group: "Content", Required: true, MaxLength: 2_000, Placeholder: "/path or https://…"},
			enumProperty("variant", "Variant", "Style", []Option{{Value: "primary", Label: "Primary"}, {Value: "secondary", Label: "Secondary"}, {Value: "outline", Label: "Outline"}, {Value: "ghost", Label: "Ghost"}}, "primary", false),
			enumProperty("size", "Size", "Style", []Option{{Value: "sm", Label: "Small"}, {Value: "md", Label: "Medium"}, {Value: "lg", Label: "Large"}}, "md", false),
			enumProperty("align", "Alignment", "Layout", textAlignment, "left", true),
			booleanProperty("fullWidth", "Full width", "Layout", false),
		},
	},
	{
		Type: "spacer", Label: "Spacer", Category: CategoryLayout, Icon: "spacer",
		Description: "Intentional empty space between components.",
		Properties:  []Property{spacingProperty("height", "Height", "Layout", "md")},
	},
	{
		Type: "navbar", Label: "Navigation bar", Category: CategoryNavigation, Icon: "navigation",
		Description: "Brand and navigation links rendered as a safe menu.",
		Properties: []Property{
			stringProperty("brand", "Brand", "Content", true, 120),
			{Key: "links", Label: "Links", Kind: PropertyArray, Group: "Content", Required: true, MinItems: 1, MaxItems: 12, ItemProperties: []Property{
				{Key: "label", Label: "Label", Kind: PropertyString, Required: true, MaxLength: 80},
				{Key: "href", Label: "Destination", Kind: PropertyURL, Required: true, MaxLength: 2_000},
			}},
			booleanProperty("sticky", "Sticky header", "Layout", false),
			colorProperty("background", "Background", "Style"),
			colorProperty("color", "Text color", "Style"),
		},
	},
	{
		Type: "form", Label: "Form", Category: CategoryForms, Icon: "form",
		Description: "A real form wired to this page's submission endpoint.",
		Properties: []Property{
			stringProperty("title", "Form title", "Content", true, 200),
			stringProperty("submitLabel", "Submit label", "Content", true, 80),
			{Key: "fields", Label: "Fields", Kind: PropertyArray, Group: "Content", Required: true, MinItems: 1, MaxItems: 12, ItemProperties: []Property{
				{Key: "name", Label: "Field name", Kind: PropertyString, Required: true, MaxLength: 40},
				{Key: "label", Label: "Label", Kind: PropertyString, Required: true, MaxLength: 120},
				enumProperty("type", "Field type", "", []Option{{Value: "text", Label: "Text"}, {Value: "email", Label: "Email"}, {Value: "tel", Label: "Telephone"}, {Value: "textarea", Label: "Long text"}}, "text", false),
				{Key: "required", Label: "Required", Kind: PropertyBoolean, Default: false},
			}},
			stringProperty("successMessage", "Success message", "Content", false, 300),
			enumProperty("align", "Alignment", "Layout", textAlignment, "left", true),
			colorProperty("background", "Background", "Style"),
			colorProperty("color", "Text color", "Style"),
		},
	},
	{
		Type: "data-text", Label: "Bound text", Category: CategoryDynamicData, Icon: "database",
		Description:    "Text resolved from an approved server-provided data source.",
		BindingAllowed: true, BindingRequired: true,
		Properties: append([]Property{
			textProperty("fallback", "Fallback text", "Content", false, 2_000),
		}, alignmentProperties()...),
	},
	{
		Type: "ad-slot", Label: "Ad slot", Category: CategoryAdvertising, Icon: "megaphone",
		Description: "A reserved, labelled advertising placement. No ad script is executed.",
		Properties: []Property{
			stringProperty("slot", "Slot ID", "Content", true, 64),
			stringProperty("label", "Disclosure label", "Content", false, 80),
			// Format and active state select the rendered placeholder itself; they
			// are not CSS properties, so they are intentionally not responsive.
			// A responsive override here would be an inspector control that cannot
			// affect the published page.
			enumProperty("format", "Format", "Layout", []Option{{Value: "leaderboard", Label: "Leaderboard"}, {Value: "rectangle", Label: "Rectangle"}, {Value: "inline", Label: "Inline"}}, "leaderboard", false),
			{Key: "active", Label: "Active", Kind: PropertyBoolean, Group: "Settings", Default: true},
		},
	},
}

var componentDefinitionsByType = func() map[string]ComponentDefinition {
	definitions := make(map[string]ComponentDefinition, len(ComponentDefinitions))
	for _, definition := range ComponentDefinitions {
		definitions[definition.Type] = definition
	}
	return definitions
}()

// LookupComponent returns the approved definition for a type.
func LookupComponent(componentType string) (ComponentDefinition, bool) {
	definition, ok := componentDefinitionsByType[componentType]
	return definition, ok
}

// DefinitionTypes lists the registered types in declaration order.
func DefinitionTypes() []string {
	types := make([]string, 0, len(ComponentDefinitions))
	for _, definition := range ComponentDefinitions {
		types = append(types, definition.Type)
	}
	return types
}

// DefaultProps returns a fresh copy of a definition's defaults.
func (d ComponentDefinition) DefaultProps() map[string]any {
	props := make(map[string]any)
	for _, property := range d.Properties {
		if property.Default != nil {
			props[property.Key] = property.Default
		}
		if property.Kind == PropertyArray {
			props[property.Key] = []any{}
		}
	}
	return props
}

// SpacingPixels returns the CSS length for a spacing token.
func SpacingPixels(token string) (string, bool) {
	value, ok := spacingPixels[token]
	return value, ok
}
