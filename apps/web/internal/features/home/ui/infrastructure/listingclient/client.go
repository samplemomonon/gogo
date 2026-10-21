// Package listingclient is the home page's remote adapter: it implements
// application/ports.Listing by talking to the product platform over the versioned
// gRPC contracts — productlisting.v1 for browse and productsearch.v1 for search —
// and application/ports.MediaResolver by talking to media-service over media.v1
// for the images the grid renders. The platform teams own those servers; this
// package owns the clients. Nothing in the home page imports a product service's
// runtime code — only this adapter knows the wire contracts, and it maps them
// into the home page's own read model. Every call carries the tenant in the
// context so the gRPC interceptor propagates it.
//
// The transport mechanics and the wire↔core mapping come from the shared
// productconsumer and mediaconsumer kits; this package keeps only what is
// decisively home's: routing a term to search instead of browse, mapping the
// neutral cores into the home read model, and resolving a page of media ids into
// the presigned URLs the product cards render.
package listingclient

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

// minSearchTermLen mirrors the search contract's minimum. A shorter term cannot be
// answered by the search read model at all (a trigram index cannot accelerate it),
// so the adapter falls back to browse rather than surfacing an error for something
// the user reasonably typed.
const minSearchTermLen = 3

// Client is the storefront's remote read model, implemented over the browse and
// search gRPC contracts, plus the media lookup that turns a product's primary
// media id into a rendered image.
type Client struct {
	product *productconsumer.Client
	media   ports.MediaResolver
	browse  *breaker.Breaker
	search  *breaker.Breaker
	logger  *slog.Logger
}

// New wraps the browse and search connections and the media resolver. Pass
// connections built by grpcx: Dial for deployed services, or InProcess for the
// same-process pairing. media and logger may be nil: without a media resolver the
// storefront degrades to no-image cards, and without a logger a media outage is
// rendered as placeholders silently.
func New(conns productconsumer.Connections, brk productconsumer.Breakers, media ports.MediaResolver, logger *slog.Logger) *Client {
	return &Client{
		product: productconsumer.New(conns, brk),
		media:   media,
		browse:  breakerFor(brk.Browse),
		search:  breakerFor(brk.Search),
		logger:  logger,
	}
}

func breakerFor(b *breaker.Breaker) *breaker.Breaker {
	if b == nil {
		return breaker.NewDefault()
	}
	return b
}

// ListPage returns one page of the storefront grid, with each product's primary
// image resolved to a presigned URL.
//
// Search and browse are different questions with different costs, answered by
// different services, so the routing decision lives here rather than in the
// handler: a term long enough for the search index goes to search, and anything
// shorter (or absent) goes to browse. The alternative — asking search to match
// everything — would mean ranking an unranked result set, which is a different
// answer than the user asked for.
func (c *Client) ListPage(ctx context.Context, q ports.Query) (results.Listing, error) {
	ctx = tenant.WithID(ctx, tenant.FromContextOrDefault(ctx))
	term := strings.TrimSpace(q.Term)

	var (
		page productconsumer.Page
		err  error
	)
	if len(term) >= minSearchTermLen {
		page, err = c.product.Search(ctx, productconsumer.Query{
			Name:   term,
			Limit:  q.Limit,
			Offset: q.Offset,
		})
	} else {
		page, err = c.product.ListPage(ctx, productconsumer.Query{
			Limit:  q.Limit,
			Offset: q.Offset,
		})
	}
	if err != nil {
		return results.Listing{}, err
	}

	products := toProducts(page.Products, tenantID(ctx))
	c.attachImages(ctx, products)
	return results.Listing{
		Products:   products,
		Total:      page.Total,
		NextCursor: page.NextCursor,
	}, nil
}

// GetProduct reads a single product from the listing projection and resolves its
// primary image. Like browse, this may briefly lag the write side while the
// projection consumes its event.
func (c *Client) GetProduct(ctx context.Context, id string) (*results.Product, error) {
	ctx = tenant.WithID(ctx, tenant.FromContextOrDefault(ctx))
	core, err := c.product.Get(ctx, id, true)
	if err != nil {
		return nil, err
	}
	product := toProduct(core, tenantID(ctx))
	c.attachImages(ctx, []*results.Product{product})
	return product, nil
}

// tenantID reads the tenant the interceptor will stamp on the outgoing call.
func tenantID(ctx context.Context) string {
	return string(tenant.FromContextOrDefault(ctx))
}

// attachImages resolves the distinct primary media ids of a page of products into
// presigned URLs and sets each product's ImageURL.
//
// Degradation is deliberate and asymmetrical: a product whose media id has no
// READY object is simply absent from the resolved map (rendered as a placeholder),
// while a media-service outage is logged and rendered as placeholders too — an
// image is decoration and must never take the grid blank. The alternative — let a
// media outage fail the whole listing — would make a fully-browsable catalogue
// unreachable over a cache-busting URL, which is the failure the D-018 guardrail
// exists to prevent.
func (c *Client) attachImages(ctx context.Context, products []*results.Product) {
	if c.media == nil || len(products) == 0 {
		return
	}
	ids := distinctMediaIDs(products)
	if len(ids) == 0 {
		return
	}
	urls, err := c.media.BatchDownloadURLs(ctx, ids)
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("product images unavailable; rendering placeholders",
				"error", err, "media_ids", len(ids))
		}
		return
	}
	for _, p := range products {
		p.ImageURL = urls[p.PrimaryMediaID]
	}
}

// distinctMediaIDs collects the non-empty primary media ids of a set of products,
// deduplicated, so a page with several products sharing one image makes a single
// media round trip.
func distinctMediaIDs(products []*results.Product) []string {
	seen := make(map[string]struct{}, len(products))
	ids := make([]string, 0, len(products))
	for _, p := range products {
		if p.PrimaryMediaID == "" {
			continue
		}
		if _, ok := seen[p.PrimaryMediaID]; ok {
			continue
		}
		seen[p.PrimaryMediaID] = struct{}{}
		ids = append(ids, p.PrimaryMediaID)
	}
	return ids
}

// toProducts maps neutral cores into the home read model.
func toProducts(cores []productconsumer.Core, tenantID string) []*results.Product {
	products := make([]*results.Product, 0, len(cores))
	for _, core := range cores {
		products = append(products, toProduct(core, tenantID))
	}
	return products
}

// toProduct maps the neutral core into the home read model.
func toProduct(core productconsumer.Core, tenantID string) *results.Product {
	return &results.Product{
		ID:             core.ID,
		ProductTypeID:  core.ProductTypeID,
		SchemaVersion:  core.SchemaVersion,
		Attributes:     core.Attributes,
		TenantID:       tenantID,
		Name:           core.Name,
		Slug:           core.Slug,
		Description:    core.Description,
		Brand:          core.Brand,
		Price:          core.Price,
		Currency:       core.Currency,
		Stock:          core.Stock,
		PrimaryMediaID: core.MediaID,
	}
}

// IsUnavailable reports whether err is the platform outage sentinel, so the
// storefront can degrade (render the grid from cache, or an explicit error page)
// instead of treating an unreachable dependency as a bug in the request.
func IsUnavailable(err error) bool {
	return errors.Is(err, productconsumer.ErrProductPlatformUnavailable)
}

// Breakers exposes the two breakers this surface depends on, so the gateway's
// metrics and readiness probe report them. They are separate because browse must
// keep serving when search is down — a storefront that cannot search is degraded,
// one that cannot browse is blank.
func (c *Client) Breakers() map[string]*breaker.Breaker {
	return map[string]*breaker.Breaker{
		string(productconsumer.UpstreamBrowse): c.browse,
		string(productconsumer.UpstreamSearch): c.search,
	}
}
