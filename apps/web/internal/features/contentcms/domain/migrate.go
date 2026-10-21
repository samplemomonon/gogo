package domain

import (
	"encoding/json"
	"fmt"
)

// Migrate converts an older, still-supported document into the current schema.
// It is deliberately explicit: immutable revisions keep their original JSON,
// and the read path upgrades them while preserving fields that are not
// interpreted by this version (under extensions).
func Migrate(document Document) (Document, error) {
	switch document.SchemaVersion {
	case SchemaVersion:
		return document, nil
	case LegacySchemaVersion:
		migrated := Document{
			SchemaVersion: SchemaVersion,
			Components:    make([]Component, 0, len(document.Components)),
			Extensions:    cloneExtensions(document.Extensions),
		}
		for i := range document.Components {
			component, err := migrateLegacyComponent(document.Components[i])
			if err != nil {
				return Document{}, fmt.Errorf("%w: components[%d]: %v", ErrInvalidDocument, i, err)
			}
			migrated.Components = append(migrated.Components, component)
		}
		return migrated, nil
	default:
		return Document{}, fmt.Errorf("%w: schemaVersion: unsupported schema version %d", ErrInvalidDocument, document.SchemaVersion)
	}
}

func migrateLegacyComponent(component Component) (Component, error) {
	migrated := Component{
		ID:         component.ID,
		Type:       component.Type,
		Props:      make(map[string]any, len(component.Props)),
		Hidden:     component.Hidden,
		Extensions: cloneExtensions(component.Extensions),
	}
	if migrated.Type == "section" {
		// v1's "section" is v2's "container". The semantic change is exactly
		// that containers accept the full approved child set.
		migrated.Type = "container"
	}
	definition, ok := LookupComponent(migrated.Type)
	if !ok {
		return Component{}, fmt.Errorf("unsupported legacy component type %q", component.Type)
	}
	known := make(map[string]struct{}, len(definition.Properties))
	for _, property := range definition.Properties {
		known[property.Key] = struct{}{}
	}
	var preserved map[string]any
	for key, value := range component.Props {
		if _, ok := known[key]; ok {
			migrated.Props[key] = value
			continue
		}
		if preserved == nil {
			preserved = make(map[string]any)
		}
		preserved[key] = value
	}
	if len(preserved) > 0 {
		encoded, err := json.Marshal(preserved)
		if err != nil {
			return Component{}, fmt.Errorf("preserve legacy properties: %w", err)
		}
		if migrated.Extensions == nil {
			migrated.Extensions = make(map[string]json.RawMessage)
		}
		// Extensions are never rendered. Keeping the old values means a
		// migration can be inspected or reversed without data loss, while the
		// closed schema still decides what is trusted.
		migrated.Extensions["legacyProps"] = encoded
	}
	for i := range component.Children {
		child, err := migrateLegacyComponent(component.Children[i])
		if err != nil {
			return Component{}, fmt.Errorf("children[%d]: %v", i, err)
		}
		migrated.Children = append(migrated.Children, child)
	}
	return migrated, nil
}

func cloneExtensions(source map[string]json.RawMessage) map[string]json.RawMessage {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}
