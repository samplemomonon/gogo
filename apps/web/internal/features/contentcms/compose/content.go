// Package compose is the contentcms vertical's composition root. It owns the
// wiring between the shell's DI container, the native page handler, and the
// PostgreSQL store, so the shell reaches the vertical only here.
package compose

import (
	"context"
	"log/slog"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/infrastructure/postgres"
	"gogo/internal/features/contentcms/presentation"
	"gogo/internal/features/contentcms/presentation/handler"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/config"
	fdi "gogo/internal/shared/foundation/di"
)

const (
	configService    = "config.AppConfig"
	parentService    = "context.parent"
	loggerService    = "slog.Logger"
	lifecycleService = "shell.Lifecycle"
	authService      = "gateway.authClient"
)

// PageHandler is the contentcms handler type used by the shell's container.
type PageHandler = handler.PageHandler

type (
	// Reader is the read-only editorial page port.
	Reader = ports.Reader
	// EditorialPage is the published editorial read model.
	EditorialPage = ports.EditorialPage
	// EditorialBlock is one published editorial block.
	EditorialBlock = ports.EditorialBlock
)

var (
	// ErrNotFound is the editorial page absent sentinel.
	ErrNotFound = ports.ErrNotFound
	// ErrUnavailable is the editorial source failure sentinel.
	ErrUnavailable = ports.ErrUnavailable
)

func configFor(c *fdi.Container) *config.AppConfig {
	return fdi.MustResolveAs[*config.AppConfig](c, configService)
}

// RegisterContentCMS publishes the vertical's Reader, native PageStore,
// PageAuthorizer, and PageHandler in the shell's container.
func RegisterContentCMS(b *fdi.Builder) {
	fdi.ProvideType[ports.Reader](b, func(c *fdi.Container) (ports.Reader, error) {
		cfg := configFor(c)
		if cfg.BuilderPublicAPIKey == "" {
			return ports.DisabledReader(), nil
		}
		reader, err := presentation.NewBuilderReader(cfg.BuilderModel, cfg.BuilderPublicAPIKey)
		return reader, err
	})

	fdi.ProvideType[ports.PageStore](b, func(c *fdi.Container) (ports.PageStore, error) {
		cfg := configFor(c)
		if cfg.DatabaseURL == "" {
			return handler.DisabledPageStore(), nil
		}
		parent := fdi.MustResolveAs[context.Context](c, parentService)
		pool, err := postgres.Connect(parent, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
		if err != nil {
			return nil, err
		}
		if err := postgres.Migrate(parent, pool); err != nil {
			pool.Close()
			return nil, err
		}
		fdi.MustResolveAs[*fdi.Lifecycle](c, lifecycleService).Add(pool.Close)
		return postgres.NewStore(pool), nil
	})

	fdi.ProvideType[ports.PageAuthorizer](b, func(c *fdi.Container) (ports.PageAuthorizer, error) {
		auth, err := fdi.ResolveAs[*authconsumer.Client](c, authService)
		if err != nil {
			return nil, err
		}
		return New(auth), nil
	})

	// The PostgreSQL store implements both ports. Keeping SiteStore separate
	// lets the shared shell ask only for published shell settings while the
	// page editor continues to own page documents and revisions.
	fdi.ProvideType[ports.SiteStore](b, func(c *fdi.Container) (ports.SiteStore, error) {
		store, err := fdi.ResolveType[ports.PageStore](c)
		if err != nil {
			return nil, err
		}
		if siteStore, ok := store.(ports.SiteStore); ok {
			return siteStore, nil
		}
		return handler.DisabledSiteStore(), nil
	})

	fdi.ProvideType[*PageHandler](b, func(c *fdi.Container) (*PageHandler, error) {
		reader, err := fdi.ResolveType[ports.Reader](c)
		if err != nil {
			return nil, err
		}
		store, err := fdi.ResolveType[ports.PageStore](c)
		if err != nil {
			return nil, err
		}
		siteStore, err := fdi.ResolveType[ports.SiteStore](c)
		if err != nil {
			return nil, err
		}
		authorizer, err := fdi.ResolveType[ports.PageAuthorizer](c)
		if err != nil {
			return nil, err
		}
		return handler.NewPageHandlerWithSite(reader, store, siteStore, authorizer, fdi.MustResolveAs[*slog.Logger](c, loggerService)), nil
	})
}
