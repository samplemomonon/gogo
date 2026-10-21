package pages

import (
	"time"

	"gogo/internal/features/contentcms/domain"
)

// SiteEditorData is the server-rendered global-shell form. Link lists are
// textarea text because a small, plain form is easier to audit than another
// client-side editor state machine; the server parses and validates every line
// before it reaches the store.
type SiteEditorData struct {
	Settings    domain.SiteSettings
	HeaderLinks string
	FooterLinks string
	Revision    int64
	Status      string
	UpdatedAt   time.Time
	PublishedAt *time.Time
	Saved       bool
	Published   bool
	Error       string
	PreviewURL  string
	PublicURL   string
}

// SitePreviewData backs the authenticated draft preview of the global shell.
type SitePreviewData struct {
	Settings domain.SiteSettings
	Revision int64
}
