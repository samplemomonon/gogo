package projection

import (
	"strings"

	"product-search-service/internal/event"
	"product-search-service/internal/model"
)

// SearchDocumentFrom is the conversion from a published product event to the
// document this service indexes.
//
// It lives in the projection rather than in search because of who needs it. The
// search use case answers queries and is deliberately wire-agnostic: it must not
// import the event package, so an engine can be moved to Elasticsearch without the
// query path learning what Kafka is. The projector, on the other hand, is the
// component whose entire job is turning a record into a read-model row, and it
// already reads the wire format. Between the two, this function is a projection
// concern — the document is a projection of the event, not of a stored row.
//
// Putting it here is what lets the storage adapter stop importing the use case.
// It used to live in search and be called from repository, which made the
// infrastructure layer depend on application code — a dependency the direction
// rules permit only for ports, not for logic.
//
// It also removes a silent-loss hazard. The conversion used to take a locally
// declared Snapshot struct that the projector filled in field by field, so adding
// a column to the event meant adding it in three places and missing one produced
// no error: the document simply lacked the field, and the omission was only
// visible as a product missing from search results. Taking the event type
// directly means the compiler reports the fields that must be considered.
func SearchDocumentFrom(snapshot *event.ProductSnapshot) *model.SearchProduct {
	doc := &model.SearchProduct{
		ID:         snapshot.ID,
		CategoryID: snapshot.CategoryID,
		Name:       snapshot.Name,
		Slug:       snapshot.Slug,
		PriceCents: snapshot.PriceCents,
		Currency:   strings.TrimSpace(snapshot.Currency),
		Stock:      snapshot.Stock,
		Status:     model.Status(snapshot.Status),
		CreatedAt:  snapshot.CreatedAt,
		UpdatedAt:  snapshot.UpdatedAt,
	}

	// The optional columns are copied rather than aliased. The snapshot comes from
	// a decoded event that the consumer may hold for the life of the batch, and a
	// stored document that shares a pointer with it would change underneath the
	// index if anything reused the decoded value.
	if snapshot.Description != nil {
		description := *snapshot.Description
		doc.Description = &description
	}
	if snapshot.Brand != nil {
		brand := *snapshot.Brand
		doc.Brand = &brand
	}
	if snapshot.PrimaryMediaID != nil {
		mediaID := *snapshot.PrimaryMediaID
		doc.PrimaryMediaID = &mediaID
	}
	return doc
}
