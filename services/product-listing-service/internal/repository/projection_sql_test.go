package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	// Registered so the DSN below is a valid postgres URL. The test skips when
	// POSTGRES_TEST_DSN is unset, so the dependency is inert by default.
	_ "github.com/lib/pq"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/database"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/event"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/projection"
)

// The read model is a projection, and its correctness has two halves. The Go half
// is unit tested with stubs. This file covers the SQL half against a real
// PostgreSQL 17, because the properties that matter most are not visible in the
// query strings: that an upsert is idempotent, that the dedup gate is one
// statement, and that the indexes shaped for browsing are the ones the planner
// actually reaches for.
//
// The schema comes from migration 0001_init.sql and the tables are truncated
// rather than dropped, so the indexes under test are the real ones.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run the read-model SQL tests against PostgreSQL")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Apply the service's own migrations rather than assuming the schema is there.
	//
	// These tests used to connect and immediately truncate tables the suite had
	// never created, so every one of them failed with "relation does not exist"
	// against a real database — which is why nobody ran them: the only way they
	// could pass was a database someone had already migrated by hand, and no code
	// said so. Migrate is idempotent and reads the embedded migrations, so a DSN is
	// the only requirement and adding a migration no longer means editing a list in
	// a test.
	if err := database.Migrate(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// clearReadModel empties the projection between tests. TRUNCATE with RESTART
// IDENTITY so the ids are predictable from one test to the next.
func clearReadModel(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(`
		TRUNCATE processed_events, product_listings,
		         product_listing_variants, consumer_checkpoints RESTART IDENTITY`,
	); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

var testClock = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// eventIDSequence produces distinct well-formed UUIDs. The last_event_id column is
// a UUID, so a placeholder like "evt-1" is rejected by PostgreSQL rather than
// stored, which is the database correctly refusing nonsense.
var eventIDSequence atomic.Int64

// nextEventID returns a distinct UUID string. The version nibble is 4 and the
// variant nibble is 8, so the value is a valid RFC 4122 version 4 UUID.
func nextEventID() string {
	n := eventIDSequence.Add(1)
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
}

// productFixture builds a snapshot with a slug derived from the name, the way the
// producer would.
func productFixture(id int64, name string) *event.ProductSnapshot {
	return &event.ProductSnapshot{
		ID:         id,
		CategoryID: 1,
		Name:       name,
		Slug:       model.Slugify(name),
		PriceCents: 12999000,
		Currency:   "IDR",
		Stock:      25,
		Status:     string(model.StatusActive),
		CreatedAt:  testClock,
		UpdatedAt:  testClock,
	}
}

func variantFixture(id, productID int64, sku string) *event.VariantSnapshot {
	return &event.VariantSnapshot{
		ID:         id,
		ProductID:  productID,
		SKU:        sku,
		Name:       sku,
		PriceCents: 1000,
		Currency:   "IDR",
		Stock:      1,
		CreatedAt:  testClock,
		UpdatedAt:  testClock,
	}
}

func envelopeFixture(id string, aggregateID int64) *event.Envelope {
	return &event.Envelope{
		EventID:       id,
		EventType:     event.TypeProductCreated,
		SchemaVersion: event.SchemaVersion,
		AggregateType: "product",
		AggregateID:   aggregateID,
		OccurredAt:    testClock,
		Payload:       []byte(`{}`),
	}
}

// A redelivered event must leave the read model in exactly the same state. This is
// the whole basis for at-least-once delivery being safe, so it is checked against
// the real upsert rather than a stub.
func TestUpsertProductIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	snapshot := productFixture(1, "Aurora Phone")
	const eventID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	if err := projector.UpsertProduct(ctx, snapshot, eventID); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first := readProductRow(t, db, 1)

	// The same event applied three more times, as a redelivering broker would.
	for i := 0; i < 3; i++ {
		if err := projector.UpsertProduct(ctx, snapshot, eventID); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	replayed := readProductRow(t, db, 1)

	if first != replayed {
		t.Errorf("replay changed the row: %+v then %+v", first, replayed)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_listings WHERE product_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1: the upsert must not duplicate the row", count)
	}
}

// A genuine update has to win over the row already there, so the upsert is not
// merely idempotent but also correct.
func TestUpsertProductAppliesAChangedSnapshot(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	if err := projector.UpsertProduct(ctx, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	changed := productFixture(1, "Aurora Phone 16")
	changed.PriceCents = 9999000
	changed.Stock = 3
	if err := projector.UpsertProduct(ctx, changed, nextEventID()); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	row := readProductRow(t, db, 1)
	if row.name != "Aurora Phone 16" {
		t.Errorf("name = %q, want the updated name", row.name)
	}
	if row.priceCents != 9999000 || row.stock != 3 {
		t.Errorf("price/stock = %d/%d, want 9999000/3", row.priceCents, row.stock)
	}
}

// A null column must round-trip as null rather than as an empty string, or a
// product that was never given a description would come back looking described.
func TestUpsertProductPreservesNullColumns(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)

	if err := projector.UpsertProduct(context.Background(),
		productFixture(1, "Plain Thing"), nextEventID()); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var (
		description sql.NullString
		brand       sql.NullString
		mediaID     sql.NullInt64
	)
	if err := db.QueryRow(`
		SELECT description, brand, primary_media_id
		FROM product_listings WHERE product_id = 1`,
	).Scan(&description, &brand, &mediaID); err != nil {
		t.Fatalf("read: %v", err)
	}
	if description.Valid || brand.Valid || mediaID.Valid {
		t.Errorf("nulls came back as values: description=%v brand=%v media=%v",
			description, brand, mediaID)
	}
}

// The dedup gate has to be a single statement, so the check and the insert cannot
// come apart. ON CONFLICT DO NOTHING returning no row is how a duplicate is
// reported, and that report is what stops a redelivery being applied twice.
func TestApplyWithPositionDeduplicates(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	const id = "11111111-2222-3333-4444-555555555555"
	envelope := envelopeFixture(id, 1)

	applied, err := projector.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 10)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if !applied {
		t.Error("the first apply reported a duplicate, want it applied")
	}

	applied, err = projector.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 10)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if applied {
		t.Error("a redelivered event reported as new, want it skipped")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("processed rows = %d, want 1", count)
	}
}

// This is the test that matters most for data loss, and it has to run against a
// real database because the behaviour under test is the rollback.
//
// The bug it pins down: recording the event as processed and applying it used to be
// two autocommit statements. product_listings.slug is UNIQUE, so a product update
// that reuses another product's slug fails at the second statement. The sequence
// then loses the event for good:
//
//	INSERT INTO processed_events ...   -- commits
//	INSERT INTO product_listings ...   -- fails on the unique constraint
//	                                    redelivery: ON CONFLICT DO NOTHING finds
//	                                    the marker, reported as a duplicate, offset
//	                                    committed, product never appears
//
// With the marker and the write in one transaction the failed delivery rolls the
// marker back, so the redelivery is treated as new and applies.
func TestAFailedWriteRollsBackTheDedupeMarker(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	// Product 1 owns the slug, so this second insert collides with it.
	if err := projector.UpsertProduct(ctx, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	colliding := productFixture(2, "Aurora Phone") // same name, therefore same slug

	eventID := nextEventID()
	envelope := envelopeFixture(eventID, 2)

	// The first delivery fails on the slug collision.
	err := projector.InTx(ctx, func(m projection.Mutations) error {
		applied, err := m.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 1)
		if err != nil {
			return err
		}
		if !applied {
			t.Fatal("the first delivery reported a duplicate, want it applied")
		}
		return m.UpsertProduct(ctx, colliding, eventID)
	})
	if err == nil {
		t.Fatal("InTx = nil, want the unique-constraint failure")
	}

	// The marker must not have survived: if it had, the redelivery below would be
	// dismissed as a duplicate and product 2 would never be projected.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events WHERE event_id = $1`,
		eventID).Scan(&count); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if count != 0 {
		t.Fatalf("the dedupe marker survived a failed write (%d rows): the redelivery "+
			"would be dismissed as a duplicate and the event lost", count)
	}

	// The redelivery, which is what the broker does after the failed attempt. It
	// must now be recognised as new.
	var redelivered bool
	err = projector.InTx(ctx, func(m projection.Mutations) error {
		var err error
		redelivered, err = m.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 1)
		return err
	})
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !redelivered {
		t.Error("the redelivery was reported as a duplicate: the marker was committed " +
			"by the failed attempt, so this event can never be applied")
	}
}

// A transaction that commits cleanly has to leave both the marker and the write in
// place, or the fix would have traded data loss for events never being recorded.
func TestASuccessfulTransactionCommitsMarkerAndWrite(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	eventID := nextEventID()
	snapshot := productFixture(7, "Nova Tablet")

	if err := projector.InTx(ctx, func(m projection.Mutations) error {
		applied, err := m.ApplyWithPosition(ctx, envelopeFixture(eventID, 7), "product.events.v1", 0, 5)
		if err != nil {
			return err
		}
		if !applied {
			t.Fatal("the first delivery reported a duplicate, want it applied")
		}
		return m.UpsertProduct(ctx, snapshot, eventID)
	}); err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var markers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events WHERE event_id = $1`,
		eventID).Scan(&markers); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 1 {
		t.Errorf("committed markers = %d, want 1", markers)
	}

	var products int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_listings WHERE product_id = 7`).Scan(&products); err != nil {
		t.Fatalf("count products: %v", err)
	}
	if products != 1 {
		t.Errorf("committed products = %d, want 1", products)
	}

	// And the marker now does its job: the next delivery is a duplicate.
	var applied bool
	if err := projector.InTx(ctx, func(m projection.Mutations) error {
		var err error
		applied, err = m.ApplyWithPosition(ctx, envelopeFixture(eventID, 7), "product.events.v1", 0, 5)
		return err
	}); err != nil {
		t.Fatalf("second delivery: %v", err)
	}
	if applied {
		t.Error("a redelivered event reported as new, want it skipped")
	}
}

// InTx must not commit when the callback fails, and must not commit twice.
func TestInTxRollsBackOnCallbackError(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	boom := errors.New("projection refused the event")
	if err := projector.InTx(ctx, func(m projection.Mutations) error {
		if _, err := m.ApplyWithPosition(ctx, envelopeFixture(nextEventID(), 1),
			"product.events.v1", 0, 1); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the callback error", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("processed rows = %d, want 0: a failed transaction must commit nothing", count)
	}
}

// The Kafka position has to be stored, or a replay cannot be traced back to the
// offset in the log that produced a row.
func TestApplyWithPositionStoresThePosition(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)

	const id = "22222222-2222-3333-4444-555555555555"
	if _, err := projector.ApplyWithPosition(context.Background(),
		envelopeFixture(id, 1), "product.events.v1", 2, 4242); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var (
		topic     string
		partition int32
		offset    int64
	)
	if err := db.QueryRow(`
		SELECT topic, partition, kafka_offset FROM processed_events WHERE event_id = $1`, id,
	).Scan(&topic, &partition, &offset); err != nil {
		t.Fatalf("read: %v", err)
	}
	if topic != "product.events.v1" || partition != 2 || offset != 4242 {
		t.Errorf("position = %s/%d/%d, want product.events.v1/2/4242", topic, partition, offset)
	}
}

// A stock event carries only the new stock, so the projection must write that one
// column. Writing the whole row would need values the event does not have, and
// would clobber a newer product update that was already projected.
func TestSetProductStockLeavesOtherColumnsAlone(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	if err := projector.UpsertProduct(ctx, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before := readProductRow(t, db, 1)

	if err := projector.SetProductStock(ctx, 1, 7, nextEventID()); err != nil {
		t.Fatalf("set stock: %v", err)
	}

	after := readProductRow(t, db, 1)
	if after.stock != 7 {
		t.Errorf("stock = %d, want 7", after.stock)
	}
	if after.name != before.name || after.priceCents != before.priceCents || after.slug != before.slug {
		t.Errorf("the stock write disturbed other columns: %+v then %+v", before, after)
	}
}

// Deleting a product that is already gone is not an error. That is what makes a
// redelivered delete harmless rather than a consumer failure that stalls the
// partition.
func TestDeleteProductIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	if err := projector.UpsertProduct(ctx, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := projector.DeleteProduct(ctx, 1); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := projector.DeleteProduct(ctx, 1); err != nil {
		t.Fatalf("second delete: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_listings WHERE product_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
}

// A list with variants must not fan out into a query per product, and each
// product must come back with its own variants rather than another product's.
func TestListAttachesVariantsToTheRightProducts(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	repo := NewListingRepository(db)
	ctx := context.Background()

	for i, id := range []int64{1, 2, 3} {
		name := "Product " + string(rune('A'+i))
		if err := projector.UpsertProduct(ctx, productFixture(id, name), nextEventID()); err != nil {
			t.Fatalf("upsert %d: %v", id, err)
		}
		for j, sku := range []string{"AAA", "BBB"} {
			variant := variantFixture(id*10+int64(j), id, sku+string(rune('A'+i)))
			if err := projector.UpsertVariant(ctx, variant, nextEventID()); err != nil {
				t.Fatalf("upsert variant: %v", err)
			}
		}
	}

	result, err := repo.List(ctx, &model.ListProductsQuery{Limit: 20, IncludeVariants: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Products) != 3 {
		t.Fatalf("got %d products, want 3", len(result.Products))
	}
	for _, product := range result.Products {
		if len(product.Variants) != 2 {
			t.Errorf("product %d has %d variants, want 2", product.ID, len(product.Variants))
		}
		for _, variant := range product.Variants {
			if variant.ProductID != product.ID {
				t.Errorf("product %d carries variant %d belonging to product %d",
					product.ID, variant.ID, variant.ProductID)
			}
		}
	}
}

// Without include_variants a page must carry none, so its size does not grow with
// the number of variants per product.
func TestListOmitsVariantsByDefault(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	repo := NewListingRepository(db)
	ctx := context.Background()

	if err := projector.UpsertProduct(ctx, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("upsert product: %v", err)
	}
	if err := projector.UpsertVariant(ctx, variantFixture(1, 1, "AAA"), nextEventID()); err != nil {
		t.Fatalf("upsert variant: %v", err)
	}

	result, err := repo.List(ctx, &model.ListProductsQuery{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(result.Products))
	}
	if result.Products[0].Variants == nil {
		t.Error("Variants is nil, want an empty slice so the JSON is []")
	}
	if len(result.Products[0].Variants) != 0 {
		t.Errorf("carried %d variants without asking for them", len(result.Products[0].Variants))
	}
}

// An empty page must render as [], and a product that was never projected as a
// clean 404. Both are depended on by the API.
func TestEmptyResultsAndMisses(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	repo := NewListingRepository(db)
	ctx := context.Background()

	result, err := repo.List(ctx, &model.ListProductsQuery{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.Products == nil {
		t.Error("Products is nil, want an empty slice")
	}
	if len(result.Products) != 0 || result.Total != 0 {
		t.Errorf("empty projection returned %d products, total %d", len(result.Products), result.Total)
	}

	if _, err := repo.GetByID(ctx, 999); !errors.Is(err, apperror.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a product that was never projected", err)
	}
}

// The total counts the matches independently of the page, or a client could never
// paginate past the first page.
func TestTotalIgnoresPaging(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	repo := NewListingRepository(db)
	ctx := context.Background()

	for i, id := range []int64{1, 2, 3, 4, 5} {
		if err := projector.UpsertProduct(ctx,
			productFixture(id, "Thing "+string(rune('A'+i))), nextEventID()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	result, err := repo.List(ctx, &model.ListProductsQuery{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Products) != 2 {
		t.Errorf("page has %d products, want 2", len(result.Products))
	}
	if result.Total != 5 {
		t.Errorf("Total = %d, want 5 regardless of the page size", result.Total)
	}
}

// The browse query is the reason the read model exists, and the partial index on
// active products is what keeps it cheap as the catalogue grows. Verified against
// the real planner, because whether an index is used is not a property of its
// definition.
//
// The bulk of drafts matters: on a tiny table PostgreSQL prefers a sequential scan
// regardless, so asserting on a small table would pass even with no index at all.
func TestBrowseQueryUsesTheActivePartialIndex(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	// Distinct names, because slug is unique across the model and a fixture that
	// reused one would fail on the constraint rather than on the plan.
	const drafts = 5000
	for i := 1; i <= drafts; i++ {
		snapshot := productFixture(int64(i), fmt.Sprintf("Draft Thing %d", i))
		snapshot.Status = string(model.StatusDraft)
		if err := projector.UpsertProduct(ctx, snapshot, nextEventID()); err != nil {
			t.Fatalf("upsert draft %d: %v", i, err)
		}
	}
	if err := projector.UpsertProduct(ctx, productFixture(90001, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("upsert active: %v", err)
	}
	if _, err := db.Exec(`ANALYZE product_listings`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT product_id FROM product_listings
		WHERE status = 'active'
		ORDER BY created_at DESC, product_id DESC
		LIMIT 20`)

	if !strings.Contains(plan, "idx_product_listings_active") {
		t.Errorf("browse plan does not use the active partial index:\n%s", plan)
	}
}

// Checkpoints are operator bookkeeping, and a second write to the same partition
// has to replace the first rather than accumulate.
func TestSaveCheckpointUpserts(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	if err := projector.SaveCheckpoint(ctx, "group", 0, 100); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := projector.SaveCheckpoint(ctx, "group", 0, 250); err != nil {
		t.Fatalf("second: %v", err)
	}

	checkpoints, err := projector.Checkpoints(ctx, "group")
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("got %d checkpoints, want 1 per partition", len(checkpoints))
	}
	if checkpoints[0].LastOffset != 250 {
		t.Errorf("offset = %d, want the latest 250", checkpoints[0].LastOffset)
	}
}

// Pruning has to drop the old rows and keep the recent ones, because a retained id
// is exactly what detects a redelivery.
func TestPruneProcessedEventsRespectsRetention(t *testing.T) {
	db := openTestDB(t)
	clearReadModel(t, db)
	projector := NewProjector(db)
	ctx := context.Background()

	if _, err := db.Exec(`
		INSERT INTO processed_events
			(event_id, event_type, aggregate_id, topic, partition, kafka_offset, processed_at)
		VALUES ('33333333-3333-3333-3333-333333333333', 'product.v1.product.created', 1,
		        't', 0, 1, CURRENT_TIMESTAMP - INTERVAL '48 hours'),
		       ('44444444-4444-4444-4444-444444444444', 'product.v1.product.created', 2,
		        't', 0, 2, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	removed, err := projector.PruneProcessedEvents(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneProcessedEvents: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d rows, want 1", removed)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("rows left = %d, want the recent one kept for deduplication", count)
	}
}

func collectPlan(t *testing.T, db *sql.DB, query string) string {
	t.Helper()

	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return plan.String()
}

type productRow struct {
	name       string
	slug       string
	priceCents int64
	stock      int
}

func readProductRow(t *testing.T, db *sql.DB, id int64) productRow {
	t.Helper()

	var row productRow
	if err := db.QueryRow(`
		SELECT name, slug, price_cents, stock
		FROM product_listings WHERE product_id = $1`, id,
	).Scan(&row.name, &row.slug, &row.priceCents, &row.stock); err != nil {
		t.Fatalf("read product %d: %v", id, err)
	}
	return row
}
