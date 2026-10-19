// Package listingclient is the home page's remote adapter: it implements
// application/ports.Listing by talking to the product platform over the versioned
// gRPC contracts — productlisting.v1 for browse and productsearch.v1 for search.
// The platform teams own those servers; this package owns the client. Nothing in
// the home page imports a product service's runtime code — only this adapter knows
// the wire contracts, and it maps them into the home page's own read model. Every
// call carries the tenant in the context so the gRPC interceptor propagates it.
//
// The transport mechanics and the wire↔core mapping come from the shared
// productconsumer kit; this package keeps only what is decisively home's: routing a
// term to search instead of browse, and mapping the neutral core into the home read
// model.
package listingclient

import (
	"context"
	"errors"
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
// search gRPC contracts. It embeds the shared consumer kit and narrows it to the
// two reads the home page performs.
type Client struct {
	product *productconsumer.Client
	browse  *breaker.Breaker
	search  *breaker.Breaker
}

// New wraps the browse and search connections. Pass connections built by grpcx:
// Dial for deployed services, or InProcess for the same-process pairing.
func New(conns productconsumer.Connections, brk productconsumer.Breakers) *Client {
	return &Client{
		product: productconsumer.New(conns, brk),
		browse:  breakerFor(brk.Browse),
		search:  breakerFor(brk.Search),
	}
}

func breakerFor(b *breaker.Breaker) *breaker.Breaker {
	if b == nil {
		return breaker.NewDefault()
	}
	return b
}

// ListPage returns one page of the storefront grid.
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

	return results.Listing{
		Products:   toProducts(page.Products, tenantID(ctx)),
		Total:      page.Total,
		NextCursor: page.NextCursor,
	}, nil
}

// GetProduct reads a single product from the listing projection. Like browse,
// this may briefly lag the write side while the projection consumes its event.
func (c *Client) GetProduct(ctx context.Context, id string) (*results.Product, error) {
	ctx = tenant.WithID(ctx, tenant.FromContextOrDefault(ctx))
	core, err := c.product.Get(ctx, id, true)
	if err != nil {
		return nil, err
	}
	return toProduct(core, tenantID(ctx)), nil
}

// tenantID reads the tenant the interceptor will stamp on the outgoing call.
func tenantID(ctx context.Context) string {
	return string(tenant.FromContextOrDefault(ctx))
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
		ID:            core.ID,
		ProductTypeID: core.ProductTypeID,
		SchemaVersion: core.SchemaVersion,
		Attributes:    core.Attributes,
		TenantID:      tenantID,
		Name:          core.Name,
		Slug:          core.Slug,
		Description:   core.Description,
		Brand:         core.Brand,
		Price:         core.Price,
		Currency:      core.Currency,
		Stock:         core.Stock,
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
