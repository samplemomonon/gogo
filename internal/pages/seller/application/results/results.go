// Package results holds the seller console's own read models: what its views
// render, mapped from the neutral cores the remote adapters produce. The page owns
// these types; no module and no service ever sees them.
package results

import "time"

// Product is the console's product read model.
//
// Field notes, because the platform's contracts do not carry everything this shape
// used to hold:
//
//   - Status is the product's lifecycle state, replacing the former Active bool. The
//     platform models three states (draft, active, archived); a bool could not
//     express "draft", which is the state a product sits in before it is sellable.
//   - Description and Brand are new: both are carried by the platform's contracts
//     and are what a product listing is expected to show.
//   - Slug is the URL-safe name the platform derives and uses for lookups.
//   - SKU, Barcode, Unit, ImageURL, BuyPrice, CostExtra, MinStock and MaxStock are
//     gone. No product contract in the platform carries them, so keeping them would
//     mean the console rendered fields that are always empty and forms that silently
//     discard what the cashier typed. Variants carry SKUs instead, which is where
//     the platform models them.
//
// Stock is deliberately absent too: the console reads stock from the inventory
// module over its own contract (see the Stock read model), which is the sole writer
// of the sellable quantity.
type Product struct {
	ID          string
	Name        string
	Slug        string
	Description string
	Brand       string
	Price       int64 // minor units
	Currency    string
	Status      string
	CategoryID  string
}

// ProductInput is what a product write carries. It is a separate type from Product
// because a write and a read are not the same shape:
//
//   - Stock is here but not on Product. A product's starting quantity is set once, at
//     creation; after that the sellable quantity belongs to gogo's inventory module,
//     which is its sole writer. Carrying Stock on the read model invited the console
//     to treat the platform's mirrored column as authoritative — which it is not, and
//     which drifts from the ledger.
//   - An update sends every field, so a blank Brand clears the brand. That is a
//     full-form edit, not a sparse patch.
type ProductInput struct {
	Name        string
	Slug        string
	Description string
	Brand       string
	Price       int64 // minor units
	Currency    string
	Stock       int
	Status      string
	CategoryID  string
}

// Category is the console's category read model, used for the product form's
// category picker. Read-only: category creation and editing belong to
// category-service.
type Category struct {
	ID   string
	Name string
}

// Stock is the console's stock read model.
type Stock struct {
	ProductID string
	Quantity  int
}

// Movement is the console's movement read model.
type Movement struct {
	ProductID string
	Kind      string
	Delta     int
	Before    int
	After     int
	Note      string
	Actor     string
	CreatedAt time.Time
}
