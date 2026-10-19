// Package catalogclient is the seller console's adapter over the product platform.
// It maps the shared productconsumer cores into the console's own read models, and
// routes each call along the platform's own seam: writes to product-service, reads to
// the browse read model.
package catalogclient

import (
	"context"
	"errors"

	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/productconsumer"
)

// Client is the console's product capability. It embeds the shared consumer kit and
// maps its neutral cores outward into the console's read models.
type Client struct {
	product *productconsumer.Client
}

// New wraps the platform connections. Pass connections built by grpcx: Dial for
// deployed services, or InProcess for a same-process pairing.
func New(conns productconsumer.Connections, brk productconsumer.Breakers) *Client {
	return &Client{product: productconsumer.New(conns, brk)}
}

// List returns the console's product page from the browse read model.
//
// This is a read, not a write, even though the console calls it "the catalogue": the
// browse read model is a projection of product-service's own event stream, so it can
// briefly trail a write that has already committed. The seller page may temporarily
// supplement this projection with the committed result of a create command after PRG;
// it does not change the ownership or source of the catalogue read model.
func (c *Client) List(ctx context.Context) ([]*results.Product, error) {
	page, err := c.product.ListPage(ctx, productconsumer.Query{Limit: consolePageSize})
	if err != nil {
		return nil, err
	}
	return toProducts(page.Products), nil
}

func (c *Client) ListProductTypes(ctx context.Context) ([]results.ProductType, error) {
	types, err := c.product.ListProductTypes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]results.ProductType, 0, len(types))
	for _, typ := range types {
		item := results.ProductType{ID: typ.ID, Name: typ.Name, SchemaVersion: typ.SchemaVersion}
		for _, attr := range typ.Attributes {
			item.Attributes = append(item.Attributes, results.ProductAttribute{Key: attr.Key, Label: attr.Label, Kind: attr.Kind, Required: attr.Required, Options: attr.Options, Unit: attr.Unit})
		}
		out = append(out, item)
	}
	return out, nil
}

// Create issues a product command carrying the starting quantity. The returned
// product is the committed row as the write side stored it.
func (c *Client) Create(ctx context.Context, input results.ProductInput) (*results.Product, error) {
	core, err := c.product.Create(ctx, toInput(input))
	if err != nil {
		return nil, err
	}
	return toProduct(core), nil
}

// Update applies a product command.
//
// Every field the console's form carries is sent, and a blank one is sent as blank
// rather than omitted: the console's update form is a full edit of the product, not a
// sparse patch. That means clearing the brand actually clears it. (The adapter's
// Patch type exists for callers that genuinely mean "change one field".)
//
// Stock is deliberately absent. The sellable quantity belongs to gogo's inventory
// module after creation, so an update that carried it would let a metadata edit
// clobber a concurrent stock adjustment — the exact hazard the platform's own
// ProductInput reserves the field to prevent.
func (c *Client) Update(ctx context.Context, id string, input results.ProductInput) (*results.Product, error) {
	name := input.Name
	slug := input.Slug
	description := input.Description
	brand := input.Brand
	price := input.Price
	currency := input.Currency
	status := productconsumer.Status(input.Status)
	categoryID := input.CategoryID

	core, err := c.product.Update(ctx, id, productconsumer.Patch{
		Name:        &name,
		Slug:        &slug,
		Description: &description,
		Brand:       &brand,
		Price:       &price,
		Currency:    &currency,
		Status:      &status,
		CategoryID:  &categoryID,
	})
	if err != nil {
		return nil, err
	}
	return toProduct(core), nil
}

// Delete removes a product.
//
// The write side reports whether a row was actually removed. A delete of something
// already gone is therefore reported as not-found rather than silently succeeding —
// the cashier pressed delete on a row that had already disappeared, and pretending
// it worked hides a real disagreement about state.
func (c *Client) Delete(ctx context.Context, id string) error {
	deleted, err := c.product.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return productconsumer.ErrNotFound
	}
	return nil
}

// ListCategories returns global and current-merchant categories for seller views.
func (c *Client) ListCategories(ctx context.Context) ([]*results.Category, error) {
	found, err := c.product.ListCategories(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]*results.Category, 0, len(found))
	for _, category := range found {
		out = append(out, &results.Category{
			ID: category.ID, Name: category.Name, ParentID: category.ParentID, IsGlobal: category.IsGlobal,
		})
	}
	return out, nil
}

// CreateMerchantCategory delegates a tenant-scoped category write to the
// category service, which remains the sole owner of the hierarchy.
func (c *Client) CreateMerchantCategory(ctx context.Context, input results.CategoryInput) (*results.Category, error) {
	category, err := c.product.CreateMerchantCategory(ctx, input.Name, "", input.ParentID)
	if err != nil {
		return nil, err
	}
	return &results.Category{
		ID: category.ID, Name: category.Name, ParentID: category.ParentID, IsGlobal: category.IsGlobal,
	}, nil
}

// consolePageSize is how many products the console lists in one page. The seller
// console is an operational surface for a single shop's catalogue, not a storefront
// grid, so it asks for one generous page rather than implementing pagination the
// platform's browse model would have to support.
const consolePageSize = 100

// toInput maps the console's write model onto a create command.
func toInput(input results.ProductInput) productconsumer.Input {
	return productconsumer.Input{
		ProductTypeID: input.ProductTypeID,
		SchemaVersion: input.SchemaVersion,
		Attributes:    input.Attributes,
		Name:          input.Name,
		Slug:          input.Slug,
		Description:   input.Description,
		Brand:         input.Brand,
		Price:         input.Price,
		Currency:      input.Currency,
		Stock:         input.Stock,
		Status:        productconsumer.Status(input.Status),
		CategoryID:    input.CategoryID,
		MerchantID:    input.MerchantID,
	}
}

func toProducts(cores []productconsumer.Core) []*results.Product {
	out := make([]*results.Product, 0, len(cores))
	for _, core := range cores {
		out = append(out, toProduct(core))
	}
	return out
}

func toProduct(core productconsumer.Core) *results.Product {
	return &results.Product{
		ID:            core.ID,
		ProductTypeID: core.ProductTypeID,
		SchemaVersion: core.SchemaVersion,
		Attributes:    core.Attributes,
		Name:          core.Name,
		Slug:          core.Slug,
		Description:   core.Description,
		Brand:         core.Brand,
		Price:         core.Price,
		Currency:      core.Currency,
		Status:        string(core.Status),
		CategoryID:    core.CategoryID,
		MerchantID:    core.MerchantID,
	}
}

// IsNotFound reports whether err means the product does not exist. The console's
// delete handler uses it to tell "someone else already deleted this" (fine, redirect)
// from a real failure.
func IsNotFound(err error) bool {
	return errors.Is(err, productconsumer.ErrNotFound)
}

// Unavailable reports whether err is a platform outage rather than a business
// answer, so a handler can log it and degrade instead of showing the cashier a form
// error.
func Unavailable(err error) bool {
	return errors.Is(err, productconsumer.ErrProductPlatformUnavailable)
}

// Detail returns the platform's own client-facing reason for a refusal, or "" — which
// is what an outage must report, because a transport error's text is for the log, not
// for a page.
func Detail(err error) string { return grpcconsumer.Detail(err) }
