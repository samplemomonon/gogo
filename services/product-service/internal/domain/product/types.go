package product

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// AttributeDefinition is the versioned metadata used by the seller form and
// product validation. This first registry is deliberately code-owned; an admin
// authoring surface can replace it without changing the product value contract.
type AttributeDefinition struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
	Unit     string   `json:"unit,omitempty"`
}

type ProductType struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	SchemaVersion int32                 `json:"schema_version"`
	Attributes    []AttributeDefinition `json:"attributes"`
}

var productTypes = []ProductType{
	{ID: "apparel", Name: "Pakaian", SchemaVersion: 1, Attributes: []AttributeDefinition{
		{Key: "material", Label: "Material", Kind: "text"},
		{Key: "weight", Label: "Berat", Kind: "number", Unit: "g"},
	}},
	{ID: "general", Name: "Produk umum", SchemaVersion: 1},
}

func ListProductTypes() []ProductType {
	out := make([]ProductType, len(productTypes))
	for i, typ := range productTypes {
		out[i] = typ
		out[i].Attributes = append([]AttributeDefinition(nil), typ.Attributes...)
	}
	return out
}

func ValidateAttributes(typeID string, version int32, values map[string]string) error {
	var typ *ProductType
	for i := range productTypes {
		if productTypes[i].ID == typeID {
			typ = &productTypes[i]
			break
		}
	}
	if typ == nil || version != typ.SchemaVersion {
		return NewValidationError("product_type_id", ErrInvalidInput)
	}
	allowed := make(map[string]AttributeDefinition, len(typ.Attributes))
	for _, definition := range typ.Attributes {
		allowed[definition.Key] = definition
	}
	for key, raw := range values {
		definition, ok := allowed[key]
		value := strings.TrimSpace(raw)
		if !ok || value == "" {
			return NewValidationError("attributes."+key, ErrInvalidInput)
		}
		if definition.Kind == "enum" && !containsString(definition.Options, value) {
			return NewValidationError("attributes."+key, fmt.Errorf("unsupported value"))
		}
		if definition.Kind == "number" {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
				return NewValidationError("attributes."+key, fmt.Errorf("must be a non-negative number"))
			}
		}
	}
	for _, definition := range typ.Attributes {
		if definition.Required && strings.TrimSpace(values[definition.Key]) == "" {
			return NewValidationError("attributes."+definition.Key, ErrInvalidInput)
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
