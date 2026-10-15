package graph

import (
	"context"

	model1 "aggregator-service/graph/model"
	"aggregator-service/internal/graph/model"
)

// The helpers in this file are the boundary between GraphQL's types and the write
// contracts'. They live in a hand-written file because gqlgen rewrites
// schema.resolvers.go on every codegen run: anything kept only there disappears the
// next time someone edits the SDL, and a resolver body that stops compiling is the
// failure mode that makes codegen feel unsafe to run.
//
// Two of them encode decisions that are not obvious from their signatures. Both are
// explained at their definitions.

// minSearchTermLength is the shortest free-text term the aggregator accepts.
//
// The search read model matches on a trigram index, which cannot accelerate a pattern
// shorter than three characters: a one or two character term falls back to a sequential
// scan of the whole index on every request. That is not a slow query, it is a scan of
// the entire index per keystroke, so a two character term is rejected outright rather
// than accepted and served expensively. Browse answers the question a short term
// usually means.
const minSearchTermLength = 3

// Page size bounds. A page is one round trip's worth of work, so the limit is what
// keeps a single request from becoming a denial of service against the read models.
const (
	// defaultLimit is used when the caller does not ask for one.
	defaultLimit int32 = 20
	// maxLimit is the largest page this service will assemble.
	maxLimit int32 = 100
)

// browseFilterFrom converts the schema's filter to the listing contract's filter.
//
// Every default here is a decision, and three of them are the difference between a
// usable API and one that quietly does the wrong thing:
//
//   - A nil filter becomes an unfiltered page. `products` with no arguments is the most
//     natural way to ask for the catalogue, and requiring a filter object for it is a
//     rule about syntax rather than about meaning.
//
//   - Variants are included unless asked otherwise. The default is the expensive one,
//     so it is stated rather than implied: a caller who does not want variant payloads
//     must say so, and a caller who does not know the flag still gets complete
//     products.
//
//   - A negative limit becomes the default and an over-large one is clamped to the
//     maximum. Both are corrections rather than refusals: the caller wants rows, and
//     the largest defensible page is a more useful answer than an error. A caller
//     paging on `total` can see the clamp in the response's own `limit` field.
//
// An inverted price range is dropped rather than forwarded. It can never match
// anything, so forwarding it produces a silent empty page, and dropping it produces the
// whole catalogue — also not what was meant, but documented and visible rather than
// indistinguishable from "no products match".
func browseFilterFrom(filter *model1.ProductFilter) BrowseFilter {
	out := BrowseFilter{
		Limit:           defaultLimit,
		IncludeVariants: true,
	}
	if filter == nil {
		return out
	}

	out.CategoryID = stringValue(filter.CategoryID)
	if filter.Status != nil {
		out.Status = *filter.Status
	}
	if filter.IncludeVariants != nil {
		out.IncludeVariants = *filter.IncludeVariants
	}

	// Money is widened here, at the boundary, rather than in the model: the schema's
	// Int is 32 bits and priceCents crosses it as an int so the model can carry the full
	// range. Money is rendered as a String in the schema for the same reason.
	out.MinPriceCents = int64Value(filter.MinPriceCents)
	out.MaxPriceCents = int64Value(filter.MaxPriceCents)
	if out.MinPriceCents > out.MaxPriceCents && out.MaxPriceCents != 0 {
		// Inverted, and not simply "max omitted", so neither bound is meaningful.
		out.MinPriceCents, out.MaxPriceCents = 0, 0
	}

	out.Limit = limitWithBounds(filter.Limit)
	out.Offset = offsetOrZero(filter.Offset)

	return out
}

// limitWithBounds clamps a caller-supplied page size.
//
// A client asking for a million rows is not asking for a million rows; it is asking
// without thinking about it. The clamp turns that into the largest page this service
// will serve, which is a bounded amount of work per request, rather than a query that
// pins a worker until the request budget runs out.
func limitWithBounds(limit *int) int32 {
	if limit == nil {
		return defaultLimit
	}
	value := int32(*limit)
	if value <= 0 {
		return defaultLimit
	}
	if value > maxLimit {
		return maxLimit
	}
	return value
}

// offsetOrZero defaults an absent or negative offset to the first page.
//
// A negative offset is corrected rather than rejected: it wraps if it reaches a service
// as an unsigned value, and the result of that is a page from an arbitrary position
// rather than an error.
func offsetOrZero(offset *int) int32 {
	if offset == nil {
		return 0
	}
	value := int32(*offset)
	if value < 0 {
		return 0
	}
	return value
}

// updateIsEmpty reports whether a product update asks for no change.
//
// An update that changes nothing is rejected by the caller rather than accepted as a
// no-op. A silent success would leave a client believing a rename happened when no write
// was issued, which is the kind of near-miss that surfaces much later.
func updateIsEmpty(input model1.UpdateProductInput) bool {
	return input.CategoryID == nil &&
		input.Name == nil &&
		input.Slug == nil &&
		input.Description == nil &&
		input.Brand == nil &&
		input.PriceCents == nil &&
		input.Currency == nil &&
		input.Stock == nil &&
		input.Status == nil &&
		input.PrimaryMediaID == nil
}

// categoryUpdateIsEmpty is updateIsEmpty for a category.
//
// Checked separately rather than by reflection over the input type: the two updates
// share a rule, not a shape, and a category has no money or stock to be wrong about.
func categoryUpdateIsEmpty(input model1.UpdateCategoryInput) bool {
	return input.Name == nil &&
		input.Description == nil &&
		input.ParentID == nil
}

// toInt32Ptr narrows an optional GraphQL Int to the contract's optional int32.
//
// The pointer is preserved rather than collapsed, because "not supplied" and "supplied
// as zero" are different instructions to the write service: the first leaves the field
// alone, the second sets it to zero.
func toInt32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	narrowed := int32(*v)
	return &narrowed
}

// int64Value widens an optional GraphQL Int to the model's int64 money type.
//
// Money is a String in the schema and an int64 here, deliberately: GraphQL's Int is 32
// bits, and priceCents above roughly 21 million minor units would be silently truncated
// by an Int. Carrying it as int64 and rendering it as a string keeps the full range.
func int64Value(v *int) int64 {
	if v == nil {
		return 0
	}
	return int64(*v)
}

// writeResult wraps a committed product with the read model's progress on it.
//
// A write goes to product-service, but a client usually reads through a read model, so
// returning only the committed row leaves it to guess whether its next read will see
// the change. The projection status answers that: false is the normal answer right
// after a create, and true means the read side has caught up.
//
// A projection status that cannot be read does not fail the write. The row is committed
// — reporting an error would invite a retry, and a retried create is a second product.
func (r *Resolver) writeResult(ctx context.Context, product *model.Product) *model.WriteProductResult {
	result := &model.WriteProductResult{Product: *product}

	status, err := r.ProjectionStore.ListingStatus(ctx)
	if err != nil {
		// The write stands. Returning an error here would invite a retry, and a
		// retried create is a second product. The failure is recorded instead, which
		// makes it visible without telling the client to do anything.
		recordError(ctx, err, "WriteProductResult.projected", "product-listing-service")
		return result
	}

	// Projected stays false, deliberately.
	//
	// The listing projection reports how many events it has consumed, not where this
	// write sits in the stream, and product-service does not return the position its
	// write produced. So "has my row landed" cannot be answered, and any non-false value
	// here would be a guess.
	//
	// A guess is worse than a negative answer. A client that reads projected=true and
	// then finds nothing on its next read concludes the write was lost and retries it,
	// and a retried create is a second product: product-service has no idempotency key.
	// Reporting the event count alongside lets a client poll the projection itself and
	// decide, which is a decision it can make with evidence.
	//
	// The field stays in the schema because it is the right shape for the answer, and
	// because removing it would be a breaking change to a field that is simply
	// conservative today. Filling it in needs a contract change on either side.
	applied := status.AppliedEvents
	result.AppliedEvents = &applied

	return result
}
