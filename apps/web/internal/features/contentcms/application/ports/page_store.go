package ports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gogo/internal/features/contentcms/domain"
)

var (
	ErrNotFound      = domain.ErrNotFound
	ErrInvalidData   = domain.ErrInvalidData
	ErrAlreadyExists = errors.New("content page already exists")
)

// ErrConflict is returned when an expected revision no longer matches. The
// concrete ConflictError carries the current revision so the API can offer a
// real recovery path instead of a generic failure.
var ErrConflict = errors.New("content page revision conflict")

type ConflictError struct {
	Expected int64
	Current  int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: expected revision %d, current revision %d", ErrConflict, e.Expected, e.Current)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

const PageManagePermission = "authz.role.manage"

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

// Page is a tenant-owned CMS page snapshot. Document is the draft
// representation; published fields remain separate so a published page never
// changes when a draft is autosaved.
type Page struct {
	TenantID    string          `json:"tenantId"`
	Slug        string          `json:"slug"`
	Title       string          `json:"title"`
	Document    domain.Document `json:"document"`
	Status      string          `json:"status"`
	Revision    int64           `json:"revision"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	PublishedAt *time.Time      `json:"publishedAt,omitempty"`
}

// Revision is one immutable snapshot. Draft saves, publishes, and restores all
// append a revision; restoring never rewrites an old row.
type Revision struct {
	TenantID  string          `json:"tenantId"`
	Slug      string          `json:"slug"`
	Revision  int64           `json:"revision"`
	Title     string          `json:"title"`
	Document  domain.Document `json:"document"`
	Status    string          `json:"status"`
	ActorID   string          `json:"actorId"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ReusableSection is a tenant-scoped component subtree. It is validated as a
// one-component document before it is stored and again when inserted.
type ReusableSection struct {
	TenantID  string           `json:"tenantId"`
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Component domain.Component `json:"component"`
	CreatedAt time.Time        `json:"createdAt"`
}

// PageStore is the tenant-scoped persistence boundary for native authored
// pages. SaveDraft and Publish use expectedRevision for optimistic concurrency;
// expectedRevision <= 0 is reserved for the legacy form path, which reads the
// current row first.
type PageStore interface {
	List(ctx context.Context, tenantID string) ([]Page, error)
	ReadDraft(ctx context.Context, tenantID, slug string) (Page, error)
	ReadPublished(ctx context.Context, tenantID, slug string) (Page, error)
	CreateDraft(ctx context.Context, tenantID, slug, title string, document domain.Document, actorID string) (Page, error)
	SaveDraft(ctx context.Context, tenantID, slug, title string, document domain.Document, expectedRevision int64, actorID string) (Page, error)
	Publish(ctx context.Context, tenantID, slug string, expectedRevision int64, actorID string) (Page, error)
	ListRevisions(ctx context.Context, tenantID, slug string, limit int) ([]Revision, error)
	ReadRevision(ctx context.Context, tenantID, slug string, revision int64) (Revision, error)
	ListSections(ctx context.Context, tenantID string) ([]ReusableSection, error)
	CreateSection(ctx context.Context, tenantID, id, name string, component domain.Component, actorID string) (ReusableSection, error)
	DeleteSection(ctx context.Context, tenantID, id string) error
	SubmitForm(ctx context.Context, tenantID, slug string, pageRevision int64, componentID string, values map[string]string) error
}
