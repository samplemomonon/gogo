package handlers

import (
	"net/http"
	"strings"

	"gogo/internal/features/sales/ui/application/results"
)

// msgInvalidForm is shown when a submitted form cannot be read. The parse error
// itself names a field and echoes the offending value, so it is never handed to the
// view; a malformed number is the cashier's typo, not a fault.
const msgInvalidForm = "Formulir tidak valid, periksa kembali isian"

const defaultProductCurrency = "IDR"

func parseCategory(r *http.Request) (results.CategoryInput, bool) {
	name := trimmed(r, "name")
	parentID := strings.TrimSpace(r.PostFormValue("parentId"))
	if name == "" || len([]rune(name)) > 255 {
		return results.CategoryInput{}, false
	}
	if parentID != "" {
		id, err := parseInt64(parentID)
		if err != nil || id <= 0 {
			return results.CategoryInput{}, false
		}
	}
	return results.CategoryInput{Name: name, ParentID: parentID}, true
}

// parseProduct reads a product form into the console's own read model.
//
// The ok result is false when a numeric field will not parse; the caller shows the
// generic form message. Blank optional numbers read as zero, so an omitted price is
// "no price yet" rather than a parse failure.
//
// Only fields the platform's product contract actually carries are read. Reading a
// field the write side does not accept would mean a form that accepts input and
// then silently discards it, which is worse than not offering the field at all.
func parseProduct(r *http.Request, available ...[]results.ProductType) (results.ProductInput, bool) {
	price, err := parseInt64(r.PostFormValue("price"))
	if err != nil {
		return results.ProductInput{}, false
	}

	stock, err := parseInt(r.PostFormValue("stock"))
	if err != nil {
		return results.ProductInput{}, false
	}

	status := trimmed(r, "status")
	if status == "" {
		// An omitted status would be stored as the platform's UNSPECIFIED, which
		// every read model renders as "no status" and no sale may include. Defaulting
		// to active here keeps the create form's default and the stored value in
		// agreement.
		status = "active"
	}

	typeID := strings.TrimSpace(r.PostFormValue("productTypeId"))
	var selected *results.ProductType
	if len(available) > 0 {
		types := available[0]
		for i := range types {
			if types[i].ID == typeID {
				selected = &types[i]
				break
			}
		}
		if selected == nil {
			return results.ProductInput{}, false
		}
		version, err := parseInt(r.PostFormValue("schemaVersion"))
		if err != nil || int32(version) != selected.SchemaVersion {
			return results.ProductInput{}, false
		}
	}
	attributes := make(map[string]string)
	var schemaVersion int32
	if selected != nil {
		schemaVersion = selected.SchemaVersion
		for _, definition := range selected.Attributes {
			if value := trimmed(r, "attribute_"+definition.Key); value != "" {
				attributes[definition.Key] = value
			}
		}
	}
	return results.ProductInput{
		ProductTypeID: typeID,
		SchemaVersion: schemaVersion,
		Attributes:    attributes,
		Name:          trimmed(r, "name"),
		Slug:          trimmed(r, "slug"),
		Description:   trimmed(r, "description"),
		Brand:         trimmed(r, "brand"),
		Price:         price,
		Currency:      defaultProductCurrency,
		Stock:         stock,
		Status:        status,
		CategoryID:    trimmed(r, "categoryId"),
	}, true
}
