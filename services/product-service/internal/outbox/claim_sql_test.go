package outbox

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	// Registered so the DSN below is a valid postgres URL. The test skips when
	// POSTGRES_TEST_DSN is unset, so the dependency is inert by default.
	_ "github.com/lib/pq"

	"product-service/internal/event"
)

// testDSN returns the DSN for the PostgreSQL instance these tests run against,
// skipping the test when it is not configured. The dependency is inert by default
// so `go test ./...` needs no database.
func testDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_TEST_DSN to run the outbox claim tests against PostgreSQL")
	}
	return dsn
}

// The claim query is the most subtle statement in this service and it cannot be
// checked by reading it. Three properties carry the whole concurrency design, and
// all three were verified against a real PostgreSQL 17:
//
//  1. It is a single statement, so the row locks it takes are released the moment
//     it returns. Nothing is held across the network call to Kafka. A session
//     holding a claim open leaves the other rows immediately claimable.
//  2. FOR UPDATE SKIP LOCKED makes a second relay skip the locked rows rather than
//     wait on them, which is what lets several relays share the table. The same
//     query without SKIP LOCKED blocked for the full duration of the other
//     session's transaction.
//  3. It excludes rows whose lease is still live. Property 2 alone is not enough,
//     because the claim commits before the publish happens: once it commits there
//     is no lock left to skip, so without the lease predicate every relay claimed
//     every pending row.
//
// The tests below run ClaimQuery itself rather than a copy. A copy is a copy of
// the query as it was, not as it is, so it kept passing while the production
// statement lost the lease predicate — which is exactly the bug it needed to catch.
const claimOnce = ClaimQuery

// openTestDB connects to the DSN in POSTGRES_TEST_DSN, skipping when it is unset.
// The schema in claimOnce is created by migration 0002_outbox.sql.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("postgres", testDSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// seedOutbox inserts n pending events and returns their row ids.
func seedOutbox(t *testing.T, db *sql.DB, n int) []int64 {
	t.Helper()

	_, err := db.Exec(`DELETE FROM outbox_events`)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}

	ids := make([]int64, 0, n)
	for i := 1; i <= n; i++ {
		// $1 is bound twice, for a BIGINT and for the text partition key. lib/pq
		// infers the type from the first use, so the cast has to be explicit:
		// without it PostgreSQL rejects the statement as an inconsistent type.
		var id int64
		err := db.QueryRow(`
			INSERT INTO outbox_events (
				event_id, topic, event_type, aggregate_type, aggregate_id,
				partition_key, payload
			)
			VALUES (gen_random_uuid(), 'product.events.v1', 'product.v1.product.created',
			        'product', $1::bigint, $1::text, '{}'::jsonb)
			RETURNING id`, i).Scan(&id)
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestClaimTakesAndReleasesItsLocks is the reason the claim is one statement
// rather than a SELECT followed by an UPDATE. Holding the locks would mean holding
// them across the publish call to Kafka, which stalls every other relay.
func TestClaimTakesAndReleasesItsLocks(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 3)

	// The claim runs in an explicit transaction that is then rolled back. If the
	// statement held its locks beyond the statement, a second session would block
	// here instead of finding rows to claim.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rows, err := tx.QueryContext(context.Background(), claimOnce, 10, 30000)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	var claimed int
	for rows.Next() {
		claimed++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("claim rows: %v", err)
	}
	_ = rows.Close()

	if claimed != 3 {
		t.Fatalf("claimed %d rows, want 3", claimed)
	}
	_ = tx.Rollback()

	// The rows are claimable again, which is only true if the locks were released.
	rows, err = db.QueryContext(context.Background(), claimOnce, 10, 30000)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var second int
	for rows.Next() {
		second++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("second claim rows: %v", err)
	}
	if second != 3 {
		t.Errorf("a second claim saw %d rows, want 3: the first claim is still holding locks", second)
	}
}

// TestClaimSkipsRatherThanBlocks is what lets several relays share the table.
// Without SKIP LOCKED a competing relay would queue behind the first one, so one
// slow publish would stall all of them.
func TestClaimSkipsRatherThanBlocks(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 4)

	// A separate connection holding two of the rows, the way a concurrent relay's
	// open transaction would. A dedicated connection is required: a transaction on
	// the same *sql.DB would not contend with itself.
	holder := mustConnect(t, db)
	defer func() { _ = holder.Close() }()

	tx, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	claimed, err := tx.QueryContext(context.Background(), claimOnce, 2, 30000)
	if err != nil {
		t.Fatalf("holder claim: %v", err)
	}
	var held int
	for claimed.Next() {
		held++
	}
	_ = claimed.Close()
	if held != 2 {
		t.Fatalf("holder claimed %d rows, want 2", held)
	}

	// The competing claim must return the other two immediately. A blocked query
	// would show up as the test's own timeout, so the context bounds it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, claimOnce, 10, 30000)
	if err != nil {
		t.Fatalf("competing claim was blocked or failed: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got int
	for rows.Next() {
		got++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("competing claim rows: %v", err)
	}
	if got != 2 {
		t.Errorf("competing claim got %d rows, want 2: the two held rows must be skipped", got)
	}
}

// A live lease has to exclude a row from a second relay's claim.
//
// This is the bug the lease existed to prevent and never did. The claim commits
// before the publish happens, so by the time a second relay runs the query there is
// no row lock left for SKIP LOCKED to skip — the predicate on leased_until is the
// only thing standing between two relays and a duplicate publish. Before it, every
// relay claimed every pending row.
//
// Measured against a real database: relay 1 claims with a 30 second lease, relay 2
// claims immediately, and relay 2 must come back empty.
func TestClaimSkipsRowsWithALiveLease(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 3)
	store := NewStore(db)
	ctx := context.Background()

	first, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("relay 1 claimed %d rows, want 3", len(first))
	}

	// The lease is still live, so the second relay must find nothing at all.
	second, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("relay 2 claimed %d rows while relay 1's lease was live, want 0: "+
			"every one of those would be published a second time", len(second))
	}
}

// Once the lease expires the row has to become claimable again, or a relay that
// died mid-publish would strand the event forever.
func TestClaimReclaimsRowsWhoseLeaseExpired(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Claim(ctx, 10, 30*time.Second); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Age the lease out rather than waiting 30 seconds for it.
	if _, err := db.Exec(
		`UPDATE outbox_events SET leased_until = CURRENT_TIMESTAMP - INTERVAL '1 second'`,
	); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}

	again, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 1 {
		t.Errorf("claimed %d rows after the lease expired, want 1: a dead relay must "+
			"not strand the event", len(again))
	}
}

// attempts counts failed publishes. Incrementing it on claim meant a row that was
// claimed repeatedly while the broker was perfectly healthy would exhaust
// MaxAttempts and be abandoned, having published successfully every time.
func TestClaimDoesNotCountAsAPublishAttempt(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)
	store := NewStore(db)
	ctx := context.Background()

	// Claim, let the lease lapse, claim again — ten times, as a contended relay
	// would. Nothing is ever published.
	for i := 0; i < 10; i++ {
		if _, err := store.Claim(ctx, 10, time.Millisecond); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if _, err := db.Exec(
			`UPDATE outbox_events SET leased_until = CURRENT_TIMESTAMP - INTERVAL '1 second'`,
		); err != nil {
			t.Fatalf("expire lease %d: %v", i, err)
		}
	}

	var attempts int
	if err := db.QueryRow(`SELECT attempts FROM outbox_events`).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d after 10 claims and no failures, want 0: the budget has "+
			"to measure delivery trouble, not relay contention", attempts)
	}
}

// A failed publish is what consumes the budget.
func TestRescheduleCountsAFailedPublish(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)
	store := NewStore(db)
	ctx := context.Background()

	claimed, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d records, want 1", len(claimed))
	}

	for want := 1; want <= 3; want++ {
		if err := store.Reschedule(ctx, claimed[0], os.ErrDeadlineExceeded,
			time.Now().Add(-time.Hour)); err != nil {
			t.Fatalf("reschedule %d: %v", want, err)
		}
		again, err := store.Claim(ctx, 10, 30*time.Second)
		if err != nil {
			t.Fatalf("claim %d: %v", want, err)
		}
		if len(again) != 1 {
			t.Fatalf("claim %d returned %d rows, want 1: the row was rescheduled into the past",
				want, len(again))
		}
		claimed = again

		var attempts int
		if err := db.QueryRow(`SELECT attempts FROM outbox_events`).Scan(&attempts); err != nil {
			t.Fatalf("read attempts: %v", err)
		}
		if attempts != want {
			t.Errorf("attempts = %d after %d failures, want %d", attempts, want, want)
		}
	}
}

// The lease guard is the point of returning the whole record rather than an id. A
// relay whose lease expired and whose row was taken over must not be able to record
// an outcome on top of the new holder's work.
func TestAnExpiredLeaseCannotRecordAnOutcome(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)
	store := NewStore(db)
	ctx := context.Background()

	stale, err := store.Claim(ctx, 10, time.Millisecond)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("claimed %d records, want 1", len(stale))
	}

	// The lease lapses and another relay takes the row.
	if _, err := db.Exec(
		`UPDATE outbox_events SET leased_until = CURRENT_TIMESTAMP - INTERVAL '1 second'`,
	); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	fresh, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("the second relay claimed %d rows, want 1", len(fresh))
	}
	if fresh[0].LeaseUntil.Equal(stale[0].LeaseUntil) {
		t.Fatal("both claims returned the same lease, want distinct values")
	}

	// The stale relay now tries to report that it published the row. It must not
	// land: the row belongs to the second relay now.
	if err := store.MarkPublished(ctx, stale); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	var published sql.NullTime
	if err := db.QueryRow(`SELECT published_at FROM outbox_events`).Scan(&published); err != nil {
		t.Fatalf("read published_at: %v", err)
	}
	if published.Valid {
		t.Error("published_at was set by a relay whose lease had expired, want it ignored: " +
			"the row was re-claimed by another relay and this one never published it")
	}
}

// mustConnect opens a second physical connection, which is what makes two sessions
// contend for the same rows. A second transaction on the same *sql.DB would not
// block itself, so the contention has to come from a separate connection.
func mustConnect(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()

	conn, err := sql.Open("postgres", testDSN(t))
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("ping second connection: %v", err)
	}
	return conn
}

// TestClaimReadsTheTopicFromTheRow guards a bug this suite actually caught: the
// claim originally did not select the topic column, so every publish went out with
// an empty topic and franz-go refused the record. The topic is stored per row
// rather than assumed, so it has to be read back with the rest of the record.
func TestClaimReadsTheTopicFromTheRow(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)

	store := NewStore(db)
	claimed, err := store.Claim(context.Background(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d records, want 1", len(claimed))
	}
	if claimed[0].Topic != "product.events.v1" {
		t.Errorf("topic = %q, want the stored product.events.v1", claimed[0].Topic)
	}
	if claimed[0].EventID == "" {
		t.Error("event id is empty, want the stored UUID")
	}
	if claimed[0].Key != "1" {
		t.Errorf("key = %q, want the stored partition key", claimed[0].Key)
	}
	// attempts counts failed publishes, so a first claim leaves it at zero. A claim
	// is this relay picking the row up, not an attempt to deliver it.
	if claimed[0].Attempts != 0 {
		t.Errorf("attempts = %d, want 0: a claim is not a failed publish", claimed[0].Attempts)
	}
	// The lease is returned so the store can guard the outcome write on it.
	if claimed[0].LeaseUntil.IsZero() {
		t.Error("LeaseUntil is zero, want the lease this claim took")
	}
}

// A row whose next attempt is in the future must not be claimed, or the backoff
// would be defeated and a struggling broker hammered in a tight loop.
func TestClaimRespectsTheBackoffWindow(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)

	if _, err := db.Exec(
		`UPDATE outbox_events SET next_attempt_at = CURRENT_TIMESTAMP + INTERVAL '1 hour'`,
	); err != nil {
		t.Fatalf("push next attempt: %v", err)
	}

	store := NewStore(db)
	claimed, err := store.Claim(context.Background(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed %d records, want 0 while the backoff window is open", len(claimed))
	}
}

// An event that has exhausted its attempts must stop being claimed. Otherwise one
// undeliverable event sits at the head of the id order and holds back every
// healthy event queued behind it, forever.
func TestClaimStopsAtFailedRows(t *testing.T) {
	db := openTestDB(t)
	ids := seedOutbox(t, db, 2)

	if _, err := db.Exec(
		`UPDATE outbox_events SET failed_at = CURRENT_TIMESTAMP WHERE id = $1`, ids[0],
	); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	store := NewStore(db)
	claimed, err := store.Claim(context.Background(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d records, want 1", len(claimed))
	}
	if claimed[0].ID == ids[0] {
		t.Error("claimed the failed row, want it left for an operator")
	}
}

// A published row must never be claimed again, otherwise every sweep would
// republish the whole history.
func TestClaimSkipsPublishedRows(t *testing.T) {
	db := openTestDB(t)
	ids := seedOutbox(t, db, 2)

	if _, err := db.Exec(
		`UPDATE outbox_events SET published_at = CURRENT_TIMESTAMP WHERE id = $1`, ids[0],
	); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	store := NewStore(db)
	claimed, err := store.Claim(context.Background(), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d records, want 1", len(claimed))
	}
	if claimed[0].ID == ids[0] {
		t.Error("claimed an already published row")
	}
}

// MarkPublished and Reschedule must move rows out of, and back into, the claimable
// set, which is what makes the relay's bookkeeping effective rather than
// decorative.
func TestMarkPublishedAndRescheduleChangeClaimability(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 2)
	store := NewStore(db)
	ctx := context.Background()

	claimed, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d, want 2", len(claimed))
	}

	// One publishes, one is pushed into the future.
	if err := store.MarkPublished(ctx, claimed[:1]); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if err := store.Reschedule(ctx, claimed[1], os.ErrDeadlineExceeded,
		time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	again, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("claimed %d records, want 0: one is published and one is in backoff", len(again))
	}

	// The failure reason has to survive, since it is the only record of why the
	// event did not go out.
	var lastError sql.NullString
	if err := db.QueryRow(
		`SELECT last_error FROM outbox_events WHERE id = $1`, claimed[1].ID,
	).Scan(&lastError); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if !lastError.Valid || lastError.String == "" {
		t.Error("last_error is empty, want the failure recorded for diagnosis")
	}
}

// Fail must take a row out of circulation permanently while keeping it as the
// audit trail that a committed change never reached the read model.
func TestFailKeepsTheRowAsAnAuditTrail(t *testing.T) {
	db := openTestDB(t)
	ids := seedOutbox(t, db, 1)
	store := NewStore(db)
	ctx := context.Background()

	// Fail is guarded on the lease, so the row has to be claimed first: that is
	// what a relay does before it can give up on an event.
	claimed, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d records, want 1", len(claimed))
	}
	if claimed[0].ID != ids[0] {
		t.Fatalf("claimed row %d, want %d", claimed[0].ID, ids[0])
	}

	if err := store.Fail(ctx, claimed[0], os.ErrDeadlineExceeded); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	var (
		failedAt  sql.NullTime
		published sql.NullTime
		payload   []byte
	)
	if err := db.QueryRow(
		`SELECT failed_at, published_at, payload FROM outbox_events WHERE id = $1`, ids[0],
	).Scan(&failedAt, &published, &payload); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !failedAt.Valid {
		t.Error("failed_at is null, want the event marked as abandoned")
	}
	if published.Valid {
		t.Error("published_at is set, want it left unset: the event never went out")
	}
	// Deleting the row would erase the only evidence that this change is missing
	// from the read model.
	if len(payload) == 0 {
		t.Error("payload was removed, want the row kept as an audit trail")
	}

	claimed, err = store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed %d records, want 0 after the event was abandoned", len(claimed))
	}
}

// Stats is what the operational picture is built on, so its numbers have to match
// what the table actually holds.
func TestStatsCountsPendingAndFailed(t *testing.T) {
	db := openTestDB(t)
	ids := seedOutbox(t, db, 3)
	store := NewStore(db)
	ctx := context.Background()

	// One published, one abandoned, one pending. Both terminal writes are guarded
	// on the lease, so they are driven from records Claim actually returned.
	claimed, err := store.Claim(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d records, want 3", len(claimed))
	}

	byID := make(map[int64]event.Record, len(claimed))
	for _, record := range claimed {
		byID[record.ID] = record
	}

	if err := store.MarkPublished(ctx, []event.Record{byID[ids[0]]}); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if err := store.Fail(ctx, byID[ids[1]], os.ErrDeadlineExceeded); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Errorf("Pending = %d, want 1", stats.Pending)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
	if stats.OldestPending == nil {
		t.Error("OldestPending is nil, want the age of the outstanding work")
	}
}

// TestClaimUsesThePartialIndex checks the reason the partial index exists.
//
// The claim is the relay's hot path and it runs every few seconds forever, so its
// cost has to stay proportional to the outstanding work rather than to the
// accumulated history. The index is partial over unpublished rows precisely for
// that, and this asserts the planner actually reaches for it once the history is
// large enough for the cost model to matter.
//
// The bulk of published rows matters: on a table with a handful of rows PostgreSQL
// prefers a sequential scan regardless, so asserting on a small table would pass
// even if the index were missing entirely.
func TestClaimUsesThePartialIndex(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 3)

	// Enough history for the cost model to prefer the index over a scan. The
	// events are marked published, so they are outside the partial index and
	// stand in for the years of history a real outbox accumulates.
	const historicalRows = 20000
	if _, err := db.Exec(`
		INSERT INTO outbox_events (
			event_id, topic, event_type, aggregate_type, aggregate_id,
			partition_key, payload, published_at
		)
		SELECT gen_random_uuid(), 'product.events.v1', 'product.v1.product.updated',
		       'product', g, g::text, '{}'::jsonb, CURRENT_TIMESTAMP
		FROM generate_series(1, $1) g`, historicalRows); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	if _, err := db.Exec(`ANALYZE outbox_events`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := explainClaim(t, db)

	if !strings.Contains(plan, "idx_outbox_pending") {
		t.Errorf("claim plan does not use idx_outbox_pending:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan") {
		t.Errorf("claim plan scans the whole table, so its cost grows with history:\n%s", plan)
	}
}

// A leased row is excluded by the predicate, and it is the *live* leases that
// matter for cost: an idle system has many NULL leases, and the index leads on
// leased_until so those rows are never visited at all.
func TestALeasedRowIsExcludedByIndexNotFilter(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 1)
	store := NewStore(db)

	if _, err := store.Claim(context.Background(), 10, 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := db.Exec(`ANALYZE outbox_events`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := explainAnalyzeClaim(t, db)
	if !strings.Contains(plan, "actual rows=0") {
		t.Errorf("a sweep examined rows while one was leased, want the lease predicate "+
			"to remove it before the heap is touched:\n%s", plan)
	}
}

// The index is partial, so an idle sweep must touch nothing. This is the property
// that makes the relay's frequent poll cheap: with everything published, the index
// it scans is empty.
func TestIdleClaimTouchesNoRows(t *testing.T) {
	db := openTestDB(t)
	seedOutbox(t, db, 3)

	if _, err := db.Exec(`UPDATE outbox_events SET published_at = CURRENT_TIMESTAMP`); err != nil {
		t.Fatalf("publish all: %v", err)
	}

	plan := explainAnalyzeClaim(t, db)
	if !strings.Contains(plan, "actual rows=0") {
		t.Errorf("an idle sweep examined rows, want the partial index to be empty:\n%s", plan)
	}
}

// explainClaim returns the query plan as text. EXPLAIN emits one row per line, so
// the lines are joined back into the shape an operator reads in psql.
//
// The candidate SELECT is explained rather than the whole ClaimQuery, because the
// UPDATE half is not what the index serves: the claim's cost is entirely in
// finding candidates.
func explainClaim(t *testing.T, db *sql.DB) string {
	t.Helper()
	return collectPlan(t, db, `
		EXPLAIN (COSTS OFF)
		SELECT id FROM outbox_events
		WHERE published_at IS NULL AND failed_at IS NULL
		  AND next_attempt_at <= CURRENT_TIMESTAMP
		  AND (leased_until IS NULL OR leased_until <= CURRENT_TIMESTAMP)
		ORDER BY id LIMIT 10 FOR UPDATE SKIP LOCKED`)
}

func explainAnalyzeClaim(t *testing.T, db *sql.DB) string {
	t.Helper()
	return collectPlan(t, db, `
		EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
		SELECT id FROM outbox_events
		WHERE published_at IS NULL AND failed_at IS NULL
		  AND next_attempt_at <= CURRENT_TIMESTAMP
		  AND (leased_until IS NULL OR leased_until <= CURRENT_TIMESTAMP)
		ORDER BY id LIMIT 10 FOR UPDATE SKIP LOCKED`)
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
