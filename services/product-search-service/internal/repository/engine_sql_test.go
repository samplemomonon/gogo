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

	"product-search-service/internal/apperror"
	"product-search-service/internal/database"
	"product-search-service/internal/event"
	"product-search-service/internal/model"
	"product-search-service/internal/projection"
	"product-search-service/internal/search"
)

// The search index is a projection built on trigram indexes, and most of what
// matters about it is not visible in the query strings. This file verifies the SQL
// half against a real PostgreSQL 17, because the properties that decide whether
// search is usable - that a term matches literally, that ranking actually ranks,
// that the index is reached - are all properties of the database answering a
// query, not of the Go that composes it.
//
// The schema comes from migration 0001_init.sql and the tables are truncated
// rather than dropped, so the indexes under test are the real ones.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run the search SQL tests against PostgreSQL")
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

// clearIndex empties the projection between tests. TRUNCATE so the indexes under
// test survive.
func clearIndex(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(
		`TRUNCATE processed_events, product_search, consumer_checkpoints`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// seedNoise loads products in one statement, for the tests that need a table big
// enough for the planner to have a reason to prefer an index.
//
// A loop of single inserts would work but takes seconds per few thousand rows,
// because every one of them is a round trip and a GIN update. The point of these
// tests is the plan the planner chooses, not how the rows arrived.
func seedNoise(t *testing.T, db *sql.DB, count int) {
	t.Helper()

	const insert = `
		INSERT INTO product_search (
			product_id, category_id, name, slug, brand,
			price_cents, currency, stock, status,
			created_at, updated_at, last_event_id
		)
		SELECT g, 1, 'Unrelated Widget ' || g, 'unrelated-widget-' || g, 'Zed',
		       1000, 'IDR', 1, 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
		       gen_random_uuid()
		FROM generate_series(100000, 100000 + $1::int - 1) g`

	if _, err := db.Exec(insert, count); err != nil {
		t.Fatalf("seed %d rows: %v", count, err)
	}
}

// analyzeAndFlush refreshes the planner's statistics and flushes the GIN pending
// list, and the flush is not optional bookkeeping.
//
// GIN writes accumulate in a pending list that every search has to walk linearly
// until autovacuum flushes it. Skipping this step is how the index test below came
// to fail convincingly: with 60k rows loaded and no flush, the index was 31MB, a
// Bitmap Index Scan was costed at 17026 against 2465 for reading the entire 13MB
// table, and the planner preferred a sequential scan - correctly, on the evidence
// it had. After one VACUUM the same query cost 38.89 and the index won. A test
// that asserted on the plan without flushing was measuring the pending list, not
// the index.
//
// In production autovacuum does this. The test does it explicitly so the
// assertion is about the steady state rather than about when the last write
// happened to be.
func analyzeAndFlush(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(`VACUUM ANALYZE product_search`); err != nil {
		t.Fatalf("vacuum analyze: %v", err)
	}
}

// newSearchService wires the real service layer in front of the engine, so the
// tests exercise the production path: term lowercasing and validation happen in
// the service, and bypassing it would test a query shape no request ever produces.
func newSearchService(engine *Engine) *search.Service {
	return search.NewService(engine)
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
		Slug:       slugify(name),
		PriceCents: 12999000,
		Currency:   "IDR",
		Stock:      25,
		Status:     string(model.StatusActive),
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

// applyInTx runs one projection the way the real projector does: the dedupe marker
// and the index write inside a single transaction.
//
// These tests are about the write half, so most of them call the mutation directly
// through a helper that still opens a transaction. That keeps them from silently
// depending on a non-transactional code path that no longer exists.
func applyInTx(t *testing.T, db *sql.DB, fn func(projection.Mutations) error) {
	t.Helper()

	if err := NewProjector(db, NewEngine(db)).InTx(context.Background(), fn); err != nil {
		t.Fatalf("projection transaction: %v", err)
	}
}

// indexProduct writes one snapshot through a transaction, returning the error so a
// test can assert on a failure.
func indexProduct(ctx context.Context, db *sql.DB, snapshot *event.ProductSnapshot, eventID string) error {
	return NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		return m.IndexProduct(ctx, snapshot, eventID)
	})
}

// setStock writes a stock value through a transaction.
func setStock(ctx context.Context, db *sql.DB, productID int64, stock int, eventID string) error {
	return NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		return m.SetProductStock(ctx, productID, stock, eventID)
	})
}

// removeProduct deletes one document through a transaction.
func removeProduct(ctx context.Context, db *sql.DB, productID int64) error {
	return NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		return m.RemoveProduct(ctx, productID)
	})
}

// slugify mirrors the write side's slug derivation.
func slugify(name string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// A term has to match literally, including the LIKE metacharacters. A bare "%"
// means a percent sign to a shopper and must not mean "match everything": the
// unescaped form would return every product in the index, which is the failure
// this test exists to pin down.
//
// Every term here carries a run of at least three letters or digits, because that
// is the service's trigram floor; a term made only of metacharacters never reaches
// SQL and has its own test below. What these cases share is a metacharacter
// surrounded by enough real text to be searchable, which is the realistic shape: a
// shopper types "50% off", not "50%".
func TestSearchMatchesLiterally(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	rows := []struct {
		id   int64
		name string
	}{
		{1, "50% Off Bundle"},
		{2, "100%_off deal"},
		{3, "A_BCD Special"},
		{4, "Aurora Phone"},
		{5, `back\slash item`},
		// The decisive rows. An unescaped "_" would match these too, so the
		// escaped term has to return row 3 alone.
		{6, "AXBCD Gadget"},
		{7, "AyBCD Gadget"},
	}
	for _, row := range rows {
		if err := indexProduct(ctx, db, productFixture(row.id, row.name), nextEventID()); err != nil {
			t.Fatalf("index %d: %v", row.id, err)
		}
	}

	cases := map[string][]int64{
		"aurora": {4},
		// Escaped, the percent sign matches only the rows that contain one.
		// Unescaped, "%" would return all seven.
		"50% off":  {1},
		"100%_off": {2},
		// Escaped, the underscore matches only row 3. Unescaped it would also
		// match rows 6 and 7, which is what makes this case worth having.
		"a_bcd":  {3},
		`back\s`: {5},
		// The search is documented as case-insensitive, which works because the
		// term is lowercased before it is compared against a lowercased column.
		// Lowering only the column would make these match nothing.
		"AURORA":               {4},
		"Aurora":               {4},
		"AuRoRa":               {4},
		"50% OFF":              {1},
		"100%_OFF":             {2},
		"A_BCD":                {3},
		"nothing matches here": nil,
	}

	for term, wantIDs := range cases {
		t.Run(term, func(t *testing.T) {
			result, err := newSearchService(engine).Search(ctx,
				&model.SearchQuery{Query: term, Limit: 20})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			got := make([]int64, 0, len(result.Products))
			for _, doc := range result.Products {
				got = append(got, doc.ID)
			}
			sortIDs(got)

			want := append([]int64(nil), wantIDs...)
			sortIDs(want)

			if len(got) != len(want) {
				t.Fatalf("matched %v, want %v: %q must not act as a wildcard", got, want, term)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("matched %v, want %v", got, want)
				}
			}
		})
	}
}

// A term whose only alphanumerics are shorter than a trigram is refused rather
// than served, and the error has to explain itself: "invalid search term" for a
// term of "%" is otherwise baffling.
func TestMetacharacterOnlyTermsAreRefusedByTheTrigramFloor(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	for _, term := range []string{"%", "_", `\`, "50%", "a_b", "%_%"} {
		_, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: term, Limit: 20})
		if !errors.Is(err, apperror.ErrInvalidInput) {
			t.Errorf("%q: err = %v, want ErrInvalidInput", term, err)
			continue
		}
		if !strings.Contains(err.Error(), "at least 3 consecutive") {
			t.Errorf("%q: err = %v, want it to explain the trigram floor", term, err)
		}
	}
}

// Ranking is the reason this is a search service rather than a filter. A term that
// is a product's whole title must outrank one buried in a long title, and the score
// has to come back non-zero for a caller to sort on it.
//
// The gaps asserted below are the measured PostgreSQL 17 values rather than a
// guess. The short exact title scores 1.0, the fragment inside a longer word about
// 0.46, and the term buried in a long title about 0.14. word_similarity, the
// obvious alternative, returns 1.0 for the first and last of those and so cannot
// order them at all; see the comment on scoreExpression.
func TestSearchRanksByRelevance(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	// "aurora" is the whole name of 2, the whole brand of 3, a fragment of the
	// word in 4, and buried in a long title in 5. Product 1 has no trigram of the
	// term in its name, slug or brand at all, so it must not be a hit.
	nonMatching := productFixture(1, "Nimbus Laptop Pro")
	title := productFixture(2, "Aurora")
	brand := productFixture(3, "Nimbus Tablet")
	brand.Brand = strPtr("Aurora")
	fragment := productFixture(4, "Auroraphone")
	longTitle := productFixture(5, "Aurora Phone 15 Pro Max Ultra Limited Edition Bundle")

	for _, snapshot := range []*event.ProductSnapshot{nonMatching, title, brand, fragment, longTitle} {
		if err := indexProduct(ctx, db, snapshot, nextEventID()); err != nil {
			t.Fatalf("index %d: %v", snapshot.ID, err)
		}
	}

	result, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Products) != 4 {
		t.Fatalf("matched %d products, want 4: %+v", len(result.Products), result.Products)
	}

	scores := map[int64]float64{}
	for _, doc := range result.Products {
		scores[doc.ID] = doc.Score
	}
	if _, ok := scores[1]; ok {
		t.Error("a product with no trigram of the term in name, slug or brand was returned")
	}
	if scores[2] != 1 {
		t.Errorf("score of the exact title = %v, want 1", scores[2])
	}
	// greatest() takes the best field, so an exact brand match ranks alongside an
	// exact title match rather than below it.
	if scores[3] != 1 {
		t.Errorf("score of the exact brand match = %v, want 1", scores[3])
	}
	if scores[4] <= 0 || scores[4] >= 1 {
		t.Errorf("score of the fragment match = %v, want between 0 and 1", scores[4])
	}
	// The gap the ranking depends on.
	if scores[5] >= scores[4] {
		t.Errorf("the long title scored %v, want below the short fragment's %v", scores[5], scores[4])
	}

	// The order has to follow the score, not the insertion or id order.
	for i := 1; i < len(result.Products); i++ {
		if result.Products[i-1].Score < result.Products[i].Score {
			t.Errorf("results are not ordered by score: %v then %v",
				result.Products[i-1].Score, result.Products[i].Score)
		}
	}
}

// A redelivered event must leave the index in exactly the same state. This is the
// whole basis for at-least-once delivery being safe, so it is checked against the
// real upsert rather than a stub.
func TestIndexIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	snapshot := productFixture(1, "Aurora Phone")
	const eventID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	if err := indexProduct(ctx, db, snapshot, eventID); err != nil {
		t.Fatalf("first index: %v", err)
	}
	first := readRow(t, db, 1)

	// The same event applied three more times, as a redelivering broker would.
	for i := 0; i < 3; i++ {
		if err := indexProduct(ctx, db, snapshot, eventID); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	replayed := readRow(t, db, 1)

	if first != replayed {
		t.Errorf("replay changed the row: %+v then %+v", first, replayed)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_search WHERE product_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1: the upsert must not duplicate the row", count)
	}
}

// A genuine update has to win over the row already there, and it has to reach the
// indexed text. A rename that updated the row but not search_text would leave the
// product findable under its old name, which is a stale index rather than a stale
// read model.
func TestIndexAppliesAChangedSnapshotIncludingTheSearchableText(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("first index: %v", err)
	}

	changed := productFixture(1, "Zephyr Phone 16")
	changed.PriceCents = 9999000
	if err := indexProduct(ctx, db, changed, nextEventID()); err != nil {
		t.Fatalf("second index: %v", err)
	}

	row := readRow(t, db, 1)
	if row.name != "Zephyr Phone 16" {
		t.Errorf("name = %q, want the updated name", row.name)
	}
	if row.priceCents != 9999000 {
		t.Errorf("price = %d, want 9999000", row.priceCents)
	}
	if !strings.Contains(row.searchText, "zephyr") {
		t.Errorf("search_text = %q, want it rebuilt from the new name", row.searchText)
	}

	// The old name must stop being findable, which is the observable consequence
	// of search_text being regenerated rather than appended to.
	result, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "zephyr", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Products) != 1 {
		t.Errorf("new name matched %d products, want 1", len(result.Products))
	}
	result, err = newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Products) != 0 {
		t.Errorf("old name still matched %d products, want 0", len(result.Products))
	}
}

// A null column must round-trip as null rather than as an empty string, or a
// product that was never given a description would come back looking described.
func TestIndexPreservesNullColumns(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Plain Thing"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}

	var (
		description sql.NullString
		brand       sql.NullString
		mediaID     sql.NullInt64
	)
	if err := db.QueryRow(`
		SELECT description, brand, primary_media_id
		FROM product_search WHERE product_id = 1`,
	).Scan(&description, &brand, &mediaID); err != nil {
		t.Fatalf("read: %v", err)
	}
	if description.Valid || brand.Valid || mediaID.Valid {
		t.Errorf("nulls came back as values: description=%v brand=%v media=%v",
			description, brand, mediaID)
	}
}

// This is the test that matters most for data loss, and it has to run against a
// real database because the behaviour under test is the rollback.
//
// The bug it pins down: recording the event as processed and writing the document
// used to be two autocommit statements. If the second failed, the marker was
// already committed, so the redelivery hit ON CONFLICT DO NOTHING, was reported as
// a duplicate, and the offset was committed — the product was never searchable and
// nothing ever retried it. With the marker and the write in one transaction the
// failed delivery rolls the marker back, so the redelivery applies.
func TestAFailedWriteRollsBackTheDedupeMarker(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	// The failure is a CHECK violation rather than a unique violation. Unlike the
	// listing read model, product_search has no UNIQUE column beyond product_id,
	// and a repeated product_id is an upsert rather than a collision, so there is
	// no constraint an ordinary bad event could trip. status must be one of
	// draft/active/archived, so a snapshot carrying anything else fails the write
	// the way a genuine data problem would.
	bad := productFixture(2, "Nova Tablet")
	bad.Status = "pending_review"

	eventID := nextEventID()
	envelope := envelopeFixture(eventID, 2)

	err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		applied, err := m.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 1)
		if err != nil {
			return err
		}
		if !applied {
			t.Fatal("the first delivery reported a duplicate, want it applied")
		}
		return m.IndexProduct(ctx, bad, eventID)
	})
	if err == nil {
		t.Fatal("InTx = nil, want the status CHECK violation")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM processed_events WHERE event_id = $1`,
		eventID).Scan(&count); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if count != 0 {
		t.Fatalf("the dedupe marker survived a failed write (%d rows): the redelivery "+
			"would be dismissed as a duplicate and the event lost", count)
	}

	// The redelivery, which is what the broker does after the failed attempt.
	var redelivered bool
	if err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		var err error
		redelivered, err = m.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 1)
		return err
	}); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !redelivered {
		t.Error("the redelivery was reported as a duplicate: the marker was committed " +
			"by the failed attempt, so this event can never be applied")
	}
}

// A transaction that commits cleanly has to leave both the marker and the document
// in place, or the fix would have traded data loss for events never being recorded.
func TestASuccessfulTransactionCommitsMarkerAndDocument(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	eventID := nextEventID()
	snapshot := productFixture(7, "Nova Tablet")

	if err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		applied, err := m.ApplyWithPosition(ctx, envelopeFixture(eventID, 7), "product.events.v1", 0, 5)
		if err != nil {
			return err
		}
		if !applied {
			t.Fatal("the first delivery reported a duplicate, want it applied")
		}
		return m.IndexProduct(ctx, snapshot, eventID)
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

	var docs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_search WHERE product_id = 7`).Scan(&docs); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docs != 1 {
		t.Errorf("committed documents = %d, want 1", docs)
	}

	// And the marker now does its job: the next delivery is a duplicate.
	var applied bool
	if err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
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

// InTx must not commit when the callback fails.
func TestInTxRollsBackOnCallbackError(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	boom := errors.New("projection refused the event")
	err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		if _, err := m.ApplyWithPosition(ctx, envelopeFixture(nextEventID(), 1),
			"product.events.v1", 0, 1); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
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

// The dedup gate has to be a single statement, so the check and the insert cannot
// come apart. ON CONFLICT DO NOTHING returning no row is how a duplicate is
// reported, and that report is what stops a redelivery being applied twice.
func TestApplyWithPositionDeduplicates(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	const id = "11111111-2222-3333-4444-555555555555"
	envelope := envelopeFixture(id, 1)

	applied, err := markProcessed(ctx, db, envelope)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if !applied {
		t.Error("the first apply reported a duplicate, want it applied")
	}

	applied, err = markProcessed(ctx, db, envelope)
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

// markProcessed records one event through a transaction, the way the projection
// reaches the dedupe gate.
func markProcessed(ctx context.Context, db *sql.DB, envelope *event.Envelope) (bool, error) {
	var applied bool
	err := NewProjector(db, NewEngine(db)).InTx(ctx, func(m projection.Mutations) error {
		var err error
		applied, err = m.ApplyWithPosition(ctx, envelope, "product.events.v1", 0, 10)
		return err
	})
	return applied, err
}

// A stock event carries only the new stock, so the projection must write that one
// column. Writing the whole row would need values the event does not have, and
// would clobber a newer product update that was already indexed.
func TestSetStockLeavesOtherColumnsAlone(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}
	before := readRow(t, db, 1)

	if err := setStock(ctx, db, 1, 7, nextEventID()); err != nil {
		t.Fatalf("set stock: %v", err)
	}

	after := readRow(t, db, 1)
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
func TestRemoveIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := removeProduct(ctx, db, 1); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if err := removeProduct(ctx, db, 1); err != nil {
		t.Fatalf("second remove: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM product_search`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
}

// A deleted product must stop being findable. This is the whole reason the search
// service consumes deletes rather than only upserts.
func TestRemovedProductStopsMatching(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := removeProduct(ctx, db, 1); err != nil {
		t.Fatalf("remove: %v", err)
	}

	result, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Products) != 0 {
		t.Errorf("a removed product still matched %d times", len(result.Products))
	}
	if result.Total != 0 {
		t.Errorf("Total = %d, want 0 after the only document was removed", result.Total)
	}
}

// The total counts the matches independently of the page, or a client could never
// page past the first page.
func TestTotalIgnoresPaging(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		snapshot := productFixture(int64(i), fmt.Sprintf("Aurora Thing %d", i))
		if err := indexProduct(ctx, db, snapshot, nextEventID()); err != nil {
			t.Fatalf("index: %v", err)
		}
	}

	result, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Products) != 2 {
		t.Errorf("page has %d products, want 2", len(result.Products))
	}
	if result.Total != 5 {
		t.Errorf("Total = %d, want 5 regardless of the page size", result.Total)
	}
}

// The filters have to compose with the text match rather than replace it, and each
// one has to actually exclude rows.
func TestFiltersNarrowTheResult(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	active := productFixture(1, "Aurora Phone")
	draft := productFixture(2, "Aurora Tablet")
	draft.Status = string(model.StatusDraft)
	other := productFixture(3, "Aurora Watch")
	other.CategoryID = 2
	// Distinguished by price AND category, so a filter on either one alone is
	// testable.
	pricey := productFixture(4, "Aurora Ultra")
	pricey.CategoryID = 2
	pricey.PriceCents = 50_000_000

	for _, snapshot := range []*event.ProductSnapshot{active, draft, other, pricey} {
		if err := indexProduct(ctx, db, snapshot, nextEventID()); err != nil {
			t.Fatalf("index: %v", err)
		}
	}

	cases := map[string]struct {
		query *model.SearchQuery
		want  []int64
	}{
		"no filter": {
			query: &model.SearchQuery{Query: "aurora", Limit: 20},
			want:  []int64{1, 2, 3, 4},
		},
		"by category": {
			query: &model.SearchQuery{Query: "aurora", CategoryID: 2, Limit: 20},
			want:  []int64{3, 4},
		},
		"by status": {
			query: &model.SearchQuery{
				Query: "aurora", Status: statusPointer(model.StatusDraft), Limit: 20,
			},
			want: []int64{2},
		},
		"by max price": {
			query: &model.SearchQuery{Query: "aurora", MaxPriceCents: pricePointer(20_000_000), Limit: 20},
			want:  []int64{1, 2, 3},
		},
		"by price band": {
			query: &model.SearchQuery{
				Query: "aurora", MinPriceCents: pricePointer(20_000_000), MaxPriceCents: pricePointer(60_000_000), Limit: 20,
			},
			want: []int64{4},
		},
		"text and filters together": {
			query: &model.SearchQuery{
				Query: "aurora", CategoryID: 1, Status: statusPointer(model.StatusActive), Limit: 20,
			},
			want: []int64{1},
		},
		"filter matches nothing": {
			query: &model.SearchQuery{Query: "aurora", CategoryID: 99, Limit: 20},
			want:  nil,
		},
		"text matches nothing": {
			query: &model.SearchQuery{Query: "nothing matches here", Limit: 20},
			want:  nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := newSearchService(engine).Search(ctx, tc.query)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			got := make([]int64, 0, len(result.Products))
			for _, doc := range result.Products {
				got = append(got, doc.ID)
			}
			sortIDs(got)
			sortIDs(tc.want)
			if len(got) != len(tc.want) {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("matched %v, want %v", got, tc.want)
				}
			}
			if result.Total != int64(len(tc.want)) {
				t.Errorf("Total = %d, want %d", result.Total, len(tc.want))
			}
		})
	}
}

// An empty index must render as an empty array, never null, or a client has to
// special-case a missing field.
func TestEmptyResultsAndMisses(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	engine := NewEngine(db)
	ctx := context.Background()

	result, err := newSearchService(engine).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Products == nil {
		t.Error("Products is nil, want an empty slice")
	}
	if len(result.Products) != 0 || result.Total != 0 {
		t.Errorf("empty index returned %d products, total %d", len(result.Products), result.Total)
	}

	if _, err := engine.GetByID(ctx, 999); !errors.Is(err, apperror.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a product that was never indexed", err)
	}
}

// The trigram index has to be the thing that answers a search, and whether the
// planner reaches for it is not a property of its definition. The corpus of
// non-matching products is what makes the question meaningful: on a small table
// PostgreSQL prefers a sequential scan whatever indexes exist, so the assertion
// would pass even with no index at all.
func TestSearchUsesTheTrigramIndex(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	seedNoise(t, db, 60000)
	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index the target: %v", err)
	}
	analyzeAndFlush(t, db)

	// The exact shape the engine runs, so the test fails if the query changes in a
	// way that stops being indexable.
	plan := collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT product_id FROM product_search
		WHERE search_text LIKE '%aurora%' ESCAPE '\'`)

	if !strings.Contains(plan, "idx_product_search_text") {
		t.Errorf("the search plan does not use the trigram index:\n%s", plan)
	}
	if !strings.Contains(plan, "Bitmap Index Scan") {
		t.Errorf("the search plan does not reach the index at all:\n%s", plan)
	}
}

// The ranking sort rides on top of the bitmap scan rather than replacing it, so
// the filters and the sort have to coexist with the index. This is the shape the
// engine actually runs, ordering by relevance rather than by id.
func TestRankedSearchStillUsesTheTrigramIndex(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	seedNoise(t, db, 60000)
	for _, name := range []string{"Aurora", "Auroraphone", "Aurora Phone 15 Pro"} {
		if err := indexProduct(ctx, db, productFixture(nextProductID(), name), nextEventID()); err != nil {
			t.Fatalf("index %q: %v", name, err)
		}
	}
	analyzeAndFlush(t, db)

	plan := collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT product_id,
		       GREATEST(similarity('aurora', name),
		               similarity('aurora', COALESCE(brand, '')),
		               similarity('aurora', slug)) AS score
		FROM product_search
		WHERE search_text LIKE '%aurora%' ESCAPE '\'
		ORDER BY score DESC, created_at DESC, product_id DESC
		LIMIT 20`)

	if !strings.Contains(plan, "idx_product_search_text") {
		t.Errorf("the ranked plan does not use the trigram index:\n%s", plan)
	}
}

// The trigram index cannot help a pattern shorter than three characters, and the
// service refuses those before they reach the database. This documents the other
// half of that rule: the refusal protects against a real sequential scan rather
// than a superstition, so the rule stays justified if it is ever questioned.
func TestShortTermsCannotUseTheTrigramIndex(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	seedNoise(t, db, 20000)
	// One document carrying the rare term, so the comparison below is between two
	// plans that both find the row and differs only in how it gets there.
	if err := indexProduct(context.Background(), db,
		productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index the target: %v", err)
	}
	analyzeAndFlush(t, db)

	// Two characters: no trigram can be extracted, so the index is not consulted.
	short := collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT product_id FROM product_search WHERE search_text LIKE '%au%' ESCAPE '\'`)
	if !strings.Contains(short, "Seq Scan") {
		t.Errorf("a two character pattern unexpectedly used an index:\n%s", short)
	}

	// Four characters, and rare in the corpus, so the index is the cheaper plan.
	// The needle deliberately appears in only one row: a term matching most of the
	// table would make a sequential scan the better plan, and asserting an index
	// there would be asserting that the planner is wrong.
	rare := collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT product_id FROM product_search WHERE search_text LIKE '%auror%' ESCAPE '\'`)
	if !strings.Contains(rare, "idx_product_search_text") {
		t.Errorf("a four character rare pattern did not use the trigram index:\n%s", rare)
	}
}

// Checkpoints are operator bookkeeping, and a second write to the same partition
// has to replace the first rather than accumulate.
func TestSaveCheckpointUpserts(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	projector := NewProjector(db, NewEngine(db))
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
	clearIndex(t, db)
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

	removed, err := NewProjector(db, NewEngine(db)).PruneProcessedEvents(ctx, 24*time.Hour)
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
	name        string
	slug        string
	searchText  string
	priceCents  int64
	stock       int
	description sql.NullString
}

func readRow(t *testing.T, db *sql.DB, id int64) productRow {
	t.Helper()

	var row productRow
	if err := db.QueryRow(`
		SELECT name, slug, search_text, price_cents, stock, description
		FROM product_search WHERE product_id = $1`, id,
	).Scan(&row.name, &row.slug, &row.searchText, &row.priceCents, &row.stock, &row.description); err != nil {
		t.Fatalf("read product %d: %v", id, err)
	}
	return row
}

// productIDSequence hands out ids for fixtures indexed alongside the seeded noise,
// which occupies the range from 100000 upwards.
var productIDSequence atomic.Int64

func nextProductID() int64 {
	return productIDSequence.Add(1)
}

func strPtr(s string) *string { return &s }

func statusPointer(s model.Status) *model.Status { return &s }

func sortIDs(ids []int64) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
}

// pricePointer builds a present price bound. The query field is a pointer so that
// "no filter" and "a bound of zero" stay distinguishable all the way to SQL.
func pricePointer(v int64) *int64 { return &v }

// The regression, in both services: a zero price bound used to be treated as an
// absent filter, so a caller asking for products costing IDR 0 or less was handed
// the entire index instead of the empty set. More rows than were requested, and no
// error to explain it.
//
// This has to run against a real database because the failure is the filter being
// absent from the WHERE clause, which a stub cannot observe.
func TestAZeroPriceBoundFiltersRatherThanDisappearing(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}

	engine := NewEngine(db)

	result, err := engine.Search(ctx, &model.SearchQuery{
		Query:         "aurora",
		MaxPriceCents: pricePointer(0),
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Total != 0 || len(result.Products) != 0 {
		t.Errorf("max_price_cents=0 returned %d products (total %d), want none: the "+
			"document costs %d cents and must not match a bound of zero",
			len(result.Products), result.Total, 12999000)
	}

	// The mirror case, and the one a shopper is most likely to send.
	result, err = engine.Search(ctx, &model.SearchQuery{
		Query:         "aurora",
		MinPriceCents: pricePointer(0),
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Total != 1 || len(result.Products) != 1 {
		t.Errorf("min_price_cents=0 returned %d products (total %d), want the 1 that "+
			"costs more than nothing", len(result.Products), result.Total)
	}
}

// An absent bound must still be absent. The fix cannot simply always add the
// condition, or an unfiltered search would suddenly cap itself at zero.
func TestAnAbsentPriceBoundAddsNoCondition(t *testing.T) {
	db := openTestDB(t)
	clearIndex(t, db)
	ctx := context.Background()

	if err := indexProduct(ctx, db, productFixture(1, "Aurora Phone"), nextEventID()); err != nil {
		t.Fatalf("index: %v", err)
	}

	result, err := NewEngine(db).Search(ctx, &model.SearchQuery{Query: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Total != 1 || len(result.Products) != 1 {
		t.Errorf("an unfiltered search returned %d products (total %d), want all 1",
			len(result.Products), result.Total)
	}
}
