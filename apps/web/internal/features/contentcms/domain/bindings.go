package domain

import "strings"

// DataBinding references one approved, server-provided value. The renderer
// resolves it from a map the server constructs; a page can never name an
// arbitrary URL, function, database query, or client-side expression.
type DataBinding struct {
	Source string `json:"source"`
	Field  string `json:"field"`
}

type DataField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"` // text | image
}

type DataSource struct {
	ID     string      `json:"id"`
	Label  string      `json:"label"`
	Fields []DataField `json:"fields"`
}

// ApprovedDataSources is the complete binding vocabulary. The editor renders
// its source/field selects from this list and the handler builds matching
// values on the server.
var ApprovedDataSources = []DataSource{
	{
		ID: "page", Label: "Page",
		Fields: []DataField{
			{ID: "title", Label: "Title", Kind: "text"},
			{ID: "slug", Label: "Slug", Kind: "text"},
		},
	},
	{
		ID: "tenant", Label: "Workspace",
		Fields: []DataField{
			{ID: "id", Label: "Workspace ID", Kind: "text"},
		},
	},
	{
		ID: "site", Label: "Site",
		Fields: []DataField{
			{ID: "name", Label: "Site name", Kind: "text"},
			{ID: "locale", Label: "Locale", Kind: "text"},
		},
	},
}

// ApprovedBinding reports whether a source/field pair is in the vocabulary.
func ApprovedBinding(source, field string) bool {
	for _, candidate := range ApprovedDataSources {
		if candidate.ID != source {
			continue
		}
		for _, candidateField := range candidate.Fields {
			if candidateField.ID == field {
				return true
			}
		}
	}
	return false
}

// BindingValues maps "source.field" to the value available to this render.
func BindingValues(pageTitle, slug, tenantID string) map[string]string {
	return BindingValuesWithSite(pageTitle, slug, tenantID, "Gogo")
}

// BindingValuesWithSite includes the tenant's published site name so components
// using site.name reflect the CMS-managed brand rather than a constant.
func BindingValuesWithSite(pageTitle, slug, tenantID, siteName string) map[string]string {
	if strings.TrimSpace(siteName) == "" {
		siteName = "Gogo"
	}
	return map[string]string{
		"page.title":  pageTitle,
		"page.slug":   slug,
		"tenant.id":   tenantID,
		"site.name":   siteName,
		"site.locale": "id-ID",
	}
}

// ResolveBinding returns the bound value and whether the binding exists.
func ResolveBinding(binding *DataBinding, values map[string]string) (string, bool) {
	if binding == nil || !ApprovedBinding(binding.Source, binding.Field) {
		return "", false
	}
	value, ok := values[binding.Source+"."+binding.Field]
	return value, ok
}
