package projection

import (
	"testing"

	"product-search-service/internal/event"
	"product-search-service/internal/model"
)

// SearchDocumentFrom is what the consumer turns an event into, so the fields a
// search result depends on have to survive the conversion, and the nullable ones
// have to stay nullable rather than becoming empty strings.
func TestSearchDocumentFrom(t *testing.T) {
	t.Parallel()

	description := "A phone"
	mediaID := int64(7)
	doc := SearchDocumentFrom(&event.ProductSnapshot{
		ID:             1,
		CategoryID:     2,
		Name:           "Aurora Phone 15 Pro",
		Slug:           "aurora-phone-15-pro",
		Description:    &description,
		Brand:          nil,
		PriceCents:     12999000,
		Currency:       " IDR ",
		Stock:          25,
		Status:         string(model.StatusActive),
		PrimaryMediaID: &mediaID,
	})

	if doc.ID != 1 || doc.CategoryID != 2 {
		t.Errorf("ids = %d/%d, want 1/2", doc.ID, doc.CategoryID)
	}
	if doc.Description == nil || *doc.Description != description {
		t.Errorf("description = %v, want the snapshot's", doc.Description)
	}
	if doc.Brand != nil {
		t.Errorf("brand = %v, want nil for a product that never had one", *doc.Brand)
	}
	if doc.PrimaryMediaID == nil || *doc.PrimaryMediaID != mediaID {
		t.Errorf("media id = %v, want %d", doc.PrimaryMediaID, mediaID)
	}
	// Currency is char(3) in the database, which pads it on the way out.
	if doc.Currency != "IDR" {
		t.Errorf("currency = %q, want it trimmed", doc.Currency)
	}
	if doc.Status != model.StatusActive {
		t.Errorf("status = %q, want active", doc.Status)
	}
}

// A search document deliberately has no variants: a page of results would then
// grow with the number of variants per product, and the document is what an
// Elasticsearch index would hold.
func TestDocumentCarriesNoVariants(t *testing.T) {
	t.Parallel()

	doc := SearchDocumentFrom(&event.ProductSnapshot{ID: 1, Name: "Aurora"})
	if doc.Score != 0 {
		t.Errorf("score = %v, want zero for a document that was not matched", doc.Score)
	}
}

// The conversion must not alias the event's optional fields. The consumer holds a
// decoded snapshot for the life of a batch, so a document sharing a pointer with
// it would be mutated underneath the index if the value were ever reused.
func TestSearchDocumentCopiesOptionalFields(t *testing.T) {
	t.Parallel()

	description := "original"
	brand := "Aurora"
	mediaID := int64(7)
	snapshot := &event.ProductSnapshot{
		ID: 1, Description: &description, Brand: &brand, PrimaryMediaID: &mediaID,
	}

	doc := SearchDocumentFrom(snapshot)

	description = "mutated after conversion"
	brand = "mutated after conversion"
	other := int64(99)
	snapshot.PrimaryMediaID = &other

	if *doc.Description != "original" {
		t.Errorf("description = %q, want the value at conversion time; the document aliases the event", *doc.Description)
	}
	if *doc.Brand != "Aurora" {
		t.Errorf("brand = %q, want the value at conversion time", *doc.Brand)
	}
	if *doc.PrimaryMediaID != 7 {
		t.Errorf("media id = %d, want 7; the document aliases the event", *doc.PrimaryMediaID)
	}
}
