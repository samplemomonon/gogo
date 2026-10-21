package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// SchemaVersion is the only schema version accepted by draft saves. Older
	// documents are migrated in place when they are read, then written back as
	// the current version.
	SchemaVersion = 2

	// LegacySchemaVersion is the v1 document shape (a closed map of string
	// properties). It is read-only: migration produces a v2 document.
	LegacySchemaVersion = 1

	MaxComponentDepth    = 6
	MaxComponentNodes    = 200
	MaxDocumentBytes     = 256_000
	MaxPropValueBytes    = 20_000
	maxComponentIDBytes  = 128
	maxPropsPerComponent = 32
	maxExtensions        = 12
	maxExtensionBytes    = 8_000
)

var (
	ErrNotFound        = errors.New("content page not found")
	ErrInvalidData     = errors.New("invalid content data")
	ErrInvalidPage     = fmt.Errorf("%w: page identity", ErrInvalidData)
	ErrInvalidDocument = fmt.Errorf("%w: document", ErrInvalidData)
)

// ValidationError is one machine-readable document problem. The editor uses
// Path to highlight the exact field; the API returns the same values so client
// validation is a convenience, never the authority.
type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// DocumentError carries every validation problem found in one pass so a save
// can report all of them at once.
type DocumentError struct {
	Errors []ValidationError `json:"errors"`
}

func (e *DocumentError) Error() string {
	if len(e.Errors) == 0 {
		return ErrInvalidDocument.Error()
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidDocument, e.Errors[0].Path, e.Errors[0].Message)
}

func (e *DocumentError) Unwrap() error { return ErrInvalidDocument }

// ValidationErrors returns the structured errors behind err, if it carries
// any. Errors constructed outside this package still report the generic
// ErrInvalidDocument shape.
func ValidationErrors(err error) []ValidationError {
	var documentErr *DocumentError
	if errors.As(err, &documentErr) {
		return append([]ValidationError(nil), documentErr.Errors...)
	}
	return nil
}

func ValidatePageIdentity(tenantID, slug, title string) error {
	if strings.TrimSpace(tenantID) == "" || len(tenantID) > 128 {
		return fmt.Errorf("%w: tenant id is required and must be at most 128 bytes", ErrInvalidPage)
	}
	if !validSlug(slug) {
		return fmt.Errorf("%w: slug must contain 1-80 lowercase letters, digits, or hyphens", ErrInvalidPage)
	}
	if strings.TrimSpace(title) == "" || !utf8.ValidString(title) || len(title) > 240 {
		return fmt.Errorf("%w: title is required, valid UTF-8, and at most 240 bytes", ErrInvalidPage)
	}
	return nil
}

func validSlug(slug string) bool {
	if slug == "" || len(slug) > 80 {
		return false
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Document is the canonical JSON page representation. schemaVersion is part of
// the stored document, not just the Go type, so an old immutable revision can
// always be migrated deliberately.
type Document struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Components    []Component                `json:"components"`
	Extensions    map[string]json.RawMessage `json:"extensions,omitempty"`
}

// Component is a node in the page tree. Props are typed by the component
// registry; their concrete Go representation is any only because JSON numbers
// and arrays arrive through encoding/json. Every save and every render validates
// against the registry before a value is trusted.
type Component struct {
	ID         string                     `json:"id"`
	Type       string                     `json:"type"`
	Props      map[string]any             `json:"props,omitempty"`
	Responsive map[string]map[string]any  `json:"responsive,omitempty"`
	Binding    *DataBinding               `json:"binding,omitempty"`
	Hidden     bool                       `json:"hidden,omitempty"`
	Children   []Component                `json:"children,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

func (d Document) Validate() error {
	migrated, err := Migrate(d)
	if err != nil {
		return err
	}
	if err := migrated.validate(); err != nil {
		return err
	}
	return nil
}

func (d Document) ValidateForPublish() error {
	migrated, err := Migrate(d)
	if err != nil {
		return err
	}
	if err := migrated.validate(); err != nil {
		return err
	}
	var errs []ValidationError
	validatePublishComponents(migrated.Components, "components", &errs)
	if len(errs) > 0 {
		return &DocumentError{Errors: errs}
	}
	return nil
}

// Normalized migrates an older document to the current schema. It is used on
// read so the editor and public renderer never see two shape variants.
func (d Document) Normalized() (Document, error) { return Migrate(d) }

func (d Document) validate() error {
	var errs []ValidationError
	if d.SchemaVersion != SchemaVersion {
		errs = append(errs, ValidationError{Path: "schemaVersion", Message: fmt.Sprintf("unsupported schema version %d (want %d)", d.SchemaVersion, SchemaVersion)})
		return &DocumentError{Errors: errs}
	}
	if err := validateExtensions(d.Extensions, "extensions", &errs); err != nil {
		return err
	}
	seenIDs := make(map[string]struct{}, MaxComponentNodes)
	nodes := 0
	var bytesUsed int
	for i := range d.Components {
		validateComponent(d.Components[i], 1, fmt.Sprintf("components[%d]", i), seenIDs, &nodes, &bytesUsed, &errs)
	}
	if bytesUsed > MaxDocumentBytes {
		errs = append(errs, ValidationError{Path: "components", Message: "property values exceed the document size limit"})
	}
	if len(errs) > 0 {
		return &DocumentError{Errors: errs}
	}
	return nil
}

func validateComponent(c Component, depth int, path string, seenIDs map[string]struct{}, nodes, bytesUsed *int, errs *[]ValidationError) {
	*nodes++
	if *nodes > MaxComponentNodes {
		*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("document exceeds %d component nodes", MaxComponentNodes)})
		return
	}
	if depth > MaxComponentDepth {
		*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("component depth exceeds %d", MaxComponentDepth)})
		return
	}
	if !validComponentID(c.ID) {
		*errs = append(*errs, ValidationError{Path: path + ".id", Message: "must start with a letter or digit and contain only letters, digits, underscores, or hyphens (max 128 bytes)"})
	} else if _, exists := seenIDs[c.ID]; exists {
		*errs = append(*errs, ValidationError{Path: path + ".id", Message: fmt.Sprintf("duplicate component id %q", c.ID)})
	} else {
		seenIDs[c.ID] = struct{}{}
	}

	definition, ok := LookupComponent(c.Type)
	if !ok {
		*errs = append(*errs, ValidationError{Path: path + ".type", Message: fmt.Sprintf("unsupported component type %q", c.Type)})
		return
	}
	if len(c.Props) > maxPropsPerComponent {
		*errs = append(*errs, ValidationError{Path: path + ".props", Message: fmt.Sprintf("component exceeds %d properties", maxPropsPerComponent)})
	}
	validateProps(definition, c.Props, path+".props", false, bytesUsed, errs)
	validateResponsive(definition, c.Responsive, path+".responsive", bytesUsed, errs)
	validateBinding(definition, c.Binding, path+".binding", errs)
	if err := validateExtensions(c.Extensions, path+".extensions", errs); err != nil {
		return
	}

	if !definition.AllowChildren && len(c.Children) > 0 {
		*errs = append(*errs, ValidationError{Path: path + ".children", Message: fmt.Sprintf("%s components cannot contain children", definition.Label)})
	}
	if definition.AllowChildren && len(definition.AllowedChildren) > 0 {
		for i, child := range c.Children {
			if !definition.allowsChild(child.Type) {
				*errs = append(*errs, ValidationError{Path: fmt.Sprintf("%s.children[%d].type", path, i), Message: fmt.Sprintf("%s cannot be nested inside %s", child.Type, definition.Label)})
			}
		}
	}
	for i := range c.Children {
		validateComponent(c.Children[i], depth+1, fmt.Sprintf("%s.children[%d]", path, i), seenIDs, nodes, bytesUsed, errs)
	}
}

var componentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validComponentID(id string) bool {
	return len(id) <= maxComponentIDBytes && componentIDPattern.MatchString(id)
}

func validateProps(definition ComponentDefinition, props map[string]any, path string, item bool, bytesUsed *int, errs *[]ValidationError) {
	known := make(map[string]Property, len(definition.Properties))
	for _, property := range definition.Properties {
		known[property.Key] = property
	}
	for key, value := range props {
		property, ok := known[key]
		if !ok {
			*errs = append(*errs, ValidationError{Path: path + "." + key, Message: "unknown property key"})
			continue
		}
		validatePropertyValue(property, value, path+"."+key, bytesUsed, errs)
	}
}

func validatePropertyValue(property Property, value any, path string, bytesUsed *int, errs *[]ValidationError) {
	if value == nil {
		*errs = append(*errs, ValidationError{Path: path, Message: "value cannot be null"})
		return
	}
	switch property.Kind {
	case PropertyString, PropertyText:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a string"})
			return
		}
		limit := property.MaxLength
		if limit == 0 {
			limit = MaxPropValueBytes
		}
		if len(text) > limit {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("must be at most %d bytes", limit)})
		}
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be valid UTF-8 without NUL bytes"})
		}
		*bytesUsed += len(property.Key) + len(text)
	case PropertyNumber:
		number, ok := numberValue(value)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a number"})
			return
		}
		if property.HasRange && (number < property.Min || number > property.Max) {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("must be between %g and %g", property.Min, property.Max)})
		}
	case PropertyBoolean:
		if _, ok := value.(bool); !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a boolean"})
		}
	case PropertyEnum:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a string enum value"})
			return
		}
		if !hasOption(property.Options, text) {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("%q is not an allowed value", text)})
		}
	case PropertyColor:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a CSS hex color"})
			return
		}
		if text != "" && !hexColorPattern.MatchString(text) {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a #RGB or #RRGGBB color"})
		}
	case PropertyImage, PropertyURL:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a URL string"})
			return
		}
		if text != "" && !SafePublicURL(text) {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be an HTTPS URL or a same-origin path"})
		}
		*bytesUsed += len(text)
	case PropertySameOriginURL:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a same-origin URL string"})
			return
		}
		if text != "" && !SafeSameOriginPath(text) {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a same-origin path"})
		}
		*bytesUsed += len(text)
	case PropertySpacing:
		text, ok := value.(string)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be a spacing token"})
			return
		}
		if _, ok := spacingPixels[text]; !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("%q is not an allowed spacing token", text)})
		}
	case PropertyArray:
		items, ok := arrayValue(value)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "must be an array"})
			return
		}
		if property.MaxItems > 0 && len(items) > property.MaxItems {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("must contain at most %d items", property.MaxItems)})
		}
		for i, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				*errs = append(*errs, ValidationError{Path: fmt.Sprintf("%s[%d]", path, i), Message: "must be an object"})
				continue
			}
			validateItemProps(property.ItemProperties, itemMap, fmt.Sprintf("%s[%d]", path, i), bytesUsed, errs)
		}
	case PropertyDataReference:
		*errs = append(*errs, ValidationError{Path: path, Message: "data references use the component binding field, not props"})
	default:
		*errs = append(*errs, ValidationError{Path: path, Message: "unsupported property type"})
	}
}

func validateItemProps(properties []Property, values map[string]any, path string, bytesUsed *int, errs *[]ValidationError) {
	known := make(map[string]Property, len(properties))
	for _, property := range properties {
		known[property.Key] = property
	}
	for key, value := range values {
		property, ok := known[key]
		if !ok {
			*errs = append(*errs, ValidationError{Path: path + "." + key, Message: "unknown object property"})
			continue
		}
		validatePropertyValue(property, value, path+"."+key, bytesUsed, errs)
	}
}

func hasOption(options []Option, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func arrayValue(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		items := make([]any, len(typed))
		for i := range typed {
			items[i] = typed[i]
		}
		return items, true
	default:
		return nil, false
	}
}

func numberValue(value any) (float64, bool) {
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
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

var (
	hexColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	extensionKey    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func validateResponsive(definition ComponentDefinition, overrides map[string]map[string]any, path string, bytesUsed *int, errs *[]ValidationError) {
	if overrides == nil {
		return
	}
	known := make(map[string]Property, len(definition.Properties))
	for _, property := range definition.Properties {
		known[property.Key] = property
	}
	for breakpoint, props := range overrides {
		if breakpoint != BreakpointTablet && breakpoint != BreakpointMobile {
			*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("unsupported breakpoint %q", breakpoint)})
			continue
		}
		for key, value := range props {
			property, ok := known[key]
			if !ok {
				*errs = append(*errs, ValidationError{Path: path + "." + breakpoint + "." + key, Message: "unknown property key"})
				continue
			}
			if !property.Responsive {
				*errs = append(*errs, ValidationError{Path: path + "." + breakpoint + "." + key, Message: "property does not support responsive overrides"})
				continue
			}
			validatePropertyValue(property, value, path+"."+breakpoint+"."+key, bytesUsed, errs)
		}
	}
}

func validateBinding(definition ComponentDefinition, binding *DataBinding, path string, errs *[]ValidationError) {
	if binding == nil {
		return
	}
	if !definition.BindingAllowed {
		*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("%s does not support data binding", definition.Label)})
		return
	}
	if !ApprovedBinding(binding.Source, binding.Field) {
		*errs = append(*errs, ValidationError{Path: path, Message: "binding must reference an approved server-provided data source"})
	}
}

func validateExtensions(extensions map[string]json.RawMessage, path string, errs *[]ValidationError) error {
	if len(extensions) > maxExtensions {
		*errs = append(*errs, ValidationError{Path: path, Message: fmt.Sprintf("at most %d extension fields are allowed", maxExtensions)})
		return nil
	}
	total := 0
	for key, value := range extensions {
		if !extensionKey.MatchString(key) {
			*errs = append(*errs, ValidationError{Path: path + "." + key, Message: "extension keys must match [A-Za-z0-9][A-Za-z0-9._-]*"})
			continue
		}
		total += len(key) + len(value)
		if !json.Valid(value) {
			*errs = append(*errs, ValidationError{Path: path + "." + key, Message: "extension value must be valid JSON"})
		}
	}
	if total > maxExtensionBytes {
		*errs = append(*errs, ValidationError{Path: path, Message: "extensions exceed the size limit"})
	}
	return nil
}

func validatePublishComponents(components []Component, path string, errs *[]ValidationError) {
	for i, component := range components {
		componentPath := fmt.Sprintf("%s[%d]", path, i)
		definition, ok := LookupComponent(component.Type)
		if !ok {
			continue
		}
		validatePublishProps(definition, component.Props, componentPath+".props", errs)
		if definition.BindingRequired && component.Binding == nil {
			*errs = append(*errs, ValidationError{Path: componentPath + ".binding", Message: fmt.Sprintf("%s requires a data source binding before publishing", definition.Label)})
		}
		validatePublishComponents(component.Children, componentPath+".children", errs)
	}
}

func validatePublishProps(definition ComponentDefinition, props map[string]any, path string, errs *[]ValidationError) {
	for _, property := range definition.Properties {
		value, present := props[property.Key]
		if property.Required && (!present || emptyPropertyValue(property, value)) {
			*errs = append(*errs, ValidationError{Path: path + "." + property.Key, Message: fmt.Sprintf("%s is required before publishing", property.Label)})
		}
		if property.Kind == PropertyArray {
			items, _ := arrayValue(value)
			if property.MinItems > 0 && len(items) < property.MinItems {
				*errs = append(*errs, ValidationError{Path: path + "." + property.Key, Message: fmt.Sprintf("%s requires at least %d item(s) before publishing", property.Label, property.MinItems)})
			}
			for i, item := range items {
				itemMap, _ := item.(map[string]any)
				for _, itemProperty := range property.ItemProperties {
					itemValue, ok := itemMap[itemProperty.Key]
					if itemProperty.Required && (!ok || emptyPropertyValue(itemProperty, itemValue)) {
						*errs = append(*errs, ValidationError{Path: fmt.Sprintf("%s.%s[%d].%s", path, property.Key, i, itemProperty.Key), Message: fmt.Sprintf("%s is required before publishing", itemProperty.Label)})
					}
				}
			}
		}
	}
}

func emptyPropertyValue(property Property, value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

// SafePublicURL reports whether raw is an HTTPS URL (with no embedded
// credentials) or a same-origin path.
func SafePublicURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return SafeSameOriginPath(raw)
	}
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != "" && u.User == nil
}

// SafeSameOriginPath limits a URL to an absolute path on this site.
func SafeSameOriginPath(raw string) bool {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "" && u.Host == "" && !strings.Contains(u.Path, "\\")
}

// ResolveProps returns the effective property set for one breakpoint. Tablet
// inherits desktop. Mobile inherits tablet, then desktop. This is the one
// inheritance rule the inspector and renderer both follow.
func (c Component) ResolveProps(breakpoint string) map[string]any {
	resolved := make(map[string]any, len(c.Props))
	for key, value := range c.Props {
		resolved[key] = value
	}
	switch breakpoint {
	case BreakpointMobile:
		for key, value := range c.Responsive[BreakpointTablet] {
			resolved[key] = value
		}
		for key, value := range c.Responsive[BreakpointMobile] {
			resolved[key] = value
		}
	case BreakpointTablet:
		for key, value := range c.Responsive[BreakpointTablet] {
			resolved[key] = value
		}
	default:
		// Desktop is the base document.
	}
	return resolved
}

// EffectiveBreakpoint returns the breakpoint whose overrides apply to a
// viewport. Unknown values fall back to desktop.
func EffectiveBreakpoint(breakpoint string) string {
	switch breakpoint {
	case BreakpointTablet, BreakpointMobile:
		return breakpoint
	default:
		return BreakpointDesktop
	}
}
