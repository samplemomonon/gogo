package pages

import (
	"github.com/a-h/templ"

	"gogo/internal/features/contentcms/domain"
	"gogo/internal/features/contentcms/presentation/components"
)

// RegisteredComponent keeps the package-level compatibility entry point used
// by existing render tests while the registry lives in components.
func RegisteredComponent(component domain.Component) templ.Component {
	return components.RenderComponent(component, components.RenderContext{})
}

func ComponentDiagnostic(componentType string) templ.Component {
	return components.ComponentDiagnostic(componentType)
}
