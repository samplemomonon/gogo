package pages

import (
	"time"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

// EditorEndpoints is the one place the client learns the authenticated API
// surface. Paths are generated server-side from the slug; the browser never
// constructs an authoring URL from document content.
type EditorEndpoints struct {
	Draft     string `json:"draft"`
	Save      string `json:"save"`
	Publish   string `json:"publish"`
	Revisions string `json:"revisions"`
	Restore   string `json:"restore"`
	Sections  string `json:"sections"`
	Preview   string `json:"preview"`
	Public    string `json:"public"`
}

type EditorPage struct {
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	Revision    int64      `json:"revision"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

// EditorBootstrap is embedded as application/json. It contains no secrets:
// session authorization remains in the HTTP-only cookie and every endpoint
// re-checks it.
type EditorBootstrap struct {
	Page        EditorPage                   `json:"page"`
	Document    domain.Document              `json:"document"`
	Definitions []domain.ComponentDefinition `json:"definitions"`
	Categories  []domain.Category            `json:"categories"`
	Presets     []domain.Preset              `json:"presets"`
	Sections    []ports.ReusableSection      `json:"sections"`
	DataSources []domain.DataSource          `json:"dataSources"`
	Values      map[string]string            `json:"values"`
	Endpoints   EditorEndpoints              `json:"endpoints"`
	Diagnostics []domain.ValidationError     `json:"diagnostics,omitempty"`
	Breakpoints []EditorBreakpoint           `json:"breakpoints"`
}

type EditorBreakpoint struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Width int    `json:"width"`
}
