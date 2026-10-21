package handler

import (
	"context"
	"errors"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

var ErrNativeStoreUnavailable = errors.New("native content store unavailable")

type disabledPageStore struct{}

func DisabledPageStore() ports.PageStore { return disabledPageStore{} }

func (disabledPageStore) List(context.Context, string) ([]ports.Page, error) {
	return nil, ErrNativeStoreUnavailable
}

func (disabledPageStore) ReadDraft(context.Context, string, string) (ports.Page, error) {
	return ports.Page{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) ReadPublished(context.Context, string, string) (ports.Page, error) {
	return ports.Page{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) CreateDraft(context.Context, string, string, string, domain.Document, string) (ports.Page, error) {
	return ports.Page{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) SaveDraft(context.Context, string, string, string, domain.Document, int64, string) (ports.Page, error) {
	return ports.Page{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) Publish(context.Context, string, string, int64, string) (ports.Page, error) {
	return ports.Page{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) ListRevisions(context.Context, string, string, int) ([]ports.Revision, error) {
	return nil, ErrNativeStoreUnavailable
}

func (disabledPageStore) ReadRevision(context.Context, string, string, int64) (ports.Revision, error) {
	return ports.Revision{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) ListSections(context.Context, string) ([]ports.ReusableSection, error) {
	return nil, ErrNativeStoreUnavailable
}

func (disabledPageStore) CreateSection(context.Context, string, string, string, domain.Component, string) (ports.ReusableSection, error) {
	return ports.ReusableSection{}, ErrNativeStoreUnavailable
}

func (disabledPageStore) DeleteSection(context.Context, string, string) error {
	return ErrNativeStoreUnavailable
}

func (disabledPageStore) SubmitForm(context.Context, string, string, int64, string, map[string]string) error {
	return ErrNativeStoreUnavailable
}
