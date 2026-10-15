# Product Search Service

**Search half of the product domain.** REST + gRPC service that finds products by
free text, served from its own trigram-indexed database, and keeps that index
current by consuming the domain events [product-service](../product-service/)
publishes.

This service holds no authoritative data. Every row is a projection of an event,
and the whole index can be rebuilt at any time by replaying the topic:

```bash
docker compose stop product-search-app
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server kafka:19092 --delete --group product-search-service
docker compose exec product-search-postgres psql -U search -d search_db \
  -c "truncate processed_events, product_search"
docker compose start product-search-app
```

## Why this is a separate service

Search used to be a `q` parameter on product-listing-service. It was moved out
because browsing and searching are different workloads with different costs:

| | product-listing-service | product-search-service |
|---|---|---|
| Question | "what is in category 3, newest first" | "what matches this text" |
| Shape | bounded indexed scan on structured columns | trigram index plus a relevance sort |
| Write cost of its indexes | cheap | one GIN entry per trigram of indexed text, on every product event |
| Ranking | none, so ordering is by date | a score per hit |
| Engine | PostgreSQL b-tree | `search.Engine`, PostgreSQL `pg_trgm` today |

Keeping them together would have meant the browse table carried a trigram index it
used only for a minority of requests, and the search code could not change on its
own schedule. Now each service pays only for the indexes its own queries need, and
the search index can be rebuilt or replaced without touching browse.

`GET /api/v1/products?q=...` on product-listing-service now answers `400` naming
this service, rather than silently ignoring the term and returning the whole
unfiltered catalogue. An ignored search term is the worst of both worlds: the
answer looks legitimate and is not.

Both services consume the same topic into separate databases, in separate consumer
groups. Separate groups are load-bearing: two services in one group would have
Kafka divide the partitions between them, so each would index roughly half the
catalogue and silently answer searches for only the products it received.

## Features

- **Relevance-ranked free-text search** - trigram match, scored by `similarity`
- **Faceted filters** - category, status and price band narrow the term matches
- **Consumes product events from Kafka** - no synchronous call to the write side
- **Idempotent projection** - a redelivered event changes nothing
- **Manual offset commits** - offsets advance only past events that were applied
- **Dead-letter topic** - a record that can never be applied does not block its
  partition
- **Rebuildable from the log** - the index is disposable by design
- **Deletes are projected** - a removed product stops being findable
- **Engine behind an interface** - OpenSearch is a new implementation, not a rewrite
- **`took_ms` on every response** - the signal to watch before search gets slow
- **Graceful shutdown** - HTTP, gRPC, consumer, pruner

## Architecture

```
product-search-service/
├── cmd/
│   └── server/
│       └── main.go                     # entrypoint, wiring, lifecycle
├── internal/
│   ├── apperror/
│   │   └── apperror.go                 # sentinel error domain
│   ├── config/
│   │   └── config.go                   # env config + validation
│   ├── database/
│   │   ├── database.go                 # connection pool, migrator
│   │   └── migrations/
│   │       └── 0001_init.sql           # index, processed events, checkpoints
│   ├── event/
│   │   └── event.go                    # decoder for the published contract
│   ├── grpcsrv/
│   │   └── search_server.go            # adapter gRPC -> service
│   ├── handler/
│   │   ├── health_handler.go           # liveness, readiness, version
│   │   ├── infrastructure_test.go
│   │   ├── middleware.go               # logging, recovery, headers
│   │   ├── search_handler.go           # HTTP search handler
│   │   └── status_handler.go           # index progress
│   ├── httpx/
│   │   └── httpx.go                    # JSON helpers, error mapping
│   ├── messaging/
│   │   ├── consumer.go                 # franz-go group consumer
│   │   └── publisher.go                # dead-letter producer
│   ├── model/
│   │   └── search.go                   # search document, query, result
│   ├── projection/
│   │   └── projection.go               # event -> search index
│   ├── repository/
│   │   ├── engine.go                   # pg_trgm engine: the replaceable half
│   │   ├── engine_sql_test.go
│   │   └── projector.go                # upserts, dedup, checkpoints
│   ├── search/
│   │   ├── service.go                  # search.Engine + the search use case
│   │   └── service_test.go
│   └── version/
│       └── version.go                  # build metadata
├── gen/searchpb/                       # generated stubs, committed
├── proto/search/v1/search.proto
├── hack/
│   └── gen-proto.sh                    # regenerate stubs
├── Dockerfile
└── docker-compose.yml
```

Dependency flow is one-way: `handler -> search -> search.Engine`, and
`consumer -> projection -> repository`. `projection` is the subtle part, so it is
tested without a broker through the `Writer` interface.

`internal/event` declares its own copy of the contract owned by product-service.
Sharing Go types would mean a dependency between two modules, which locks the
deploy order of both; a duplicated struct is cheaper, and a mismatch shows up as a
decode error in the consumer rather than a compile error.

## Replacing the engine with OpenSearch or Elasticsearch

`search.Engine` is the seam. It has four methods and the whole service is built
above it:

```go
type Engine interface {
	Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error)
	Index(ctx context.Context, doc *model.SearchProduct, eventID string) error
	Remove(ctx context.Context, productID int64) error
	Count(ctx context.Context) (int64, error)
}
```

What is deliberately *not* engine-specific, so the swap does not reach it:

- **Validation** - paging, filters, term rules all live in `search.Service`
- **The response envelope** - a page, an unpaged total, the applied page, and a
  score per hit
- **The document shape** - `DocumentFromSnapshot` is what an index request body
  would be built from
- **The HTTP and gRPC contracts**, including the `score` field

Two things are acknowledged as PostgreSQL-shaped and would change:

1. **`SetStock` writes one column.** It is on the `Indexer` interface the projector
   uses, not on `search.Engine`, because a real search engine has no cheap partial
   update: an OpenSearch implementation would refetch the document or carry stock
   on its own field. Keeping it on the write side of the boundary makes that
   visible instead of surprising.

2. **The score's scale.** `similarity()` returns 0 to 1 and is documented as
   comparable only between hits of one response. BM25 is unbounded. The API
   contract says so, so a client must not compare scores across queries - and
   `took_ms` is the field to watch when deciding the move is due.

Facets are not implemented. When they are, the research says to compute them in a
separate aggregate query rather than inside the search query, so the hot path stays
fast in both engines.

## Delivery semantics

The consumer commits manually and deduplicates on event id, so the pair gives
**at-least-once delivery with an exactly-once effect on the index**:

- Offsets are committed **after** an event is applied. A failed commit means the
  batch is re-read and the projector's idempotency turns it into a no-op.
- If one record fails transiently, the offset must **not** pass it. Only the prefix
  already handled is committed. Committing the whole batch would step over an event
  the index never saw, and that event would be gone for good.
- Deduplication happens first, so a redelivery costs one indexed lookup and no
  write.
- Unknown event types are skipped **without** being marked processed. A newer
  producer may legitimately emit events this version does not know, and recording
  them would discard them permanently if this service were later rolled back to
  understand them.

### Dead letter

Records that can never be applied - malformed envelopes, a schema version that does
not match - go to `product.events.v1.dlq` with their original position as headers,
and their offset is committed. Without that, one poisoned record holds its
partition forever and every product behind it stays unsearchable.

Transient failures do **not** go to the dead letter. They back off and are re-read,
because the cause is usually an index that is merely unreachable and will recover.

### Variant events are decoded, then ignored

A search result is a product, not a product with its variant list, so a variant has
nothing to update. The events are still recognised rather than skipped blindly, so
an unparseable variant payload is a dead-lettered failure instead of a silent no-op
that would hide a broken producer.

## Quick start

Needs its own PostgreSQL and a Kafka broker:

```bash
docker compose up -d --build
```

To use the same stack and broker as the repository root:

```bash
docker compose -f ../docker-compose.yml up -d --build
```

Run from the host instead:

```bash
KAFKA_BROKERS=host.docker.internal:9092 docker compose up -d --build
```

## API

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health`, `/healthz` | liveness, no dependency check |
| `GET` | `/readyz` | readiness, database ping with a 3s timeout |
| `GET` | `/version` | version and build info |

`/readyz` deliberately does **not** check Kafka. A consumer that has fallen behind
still serves correct data, just older, and pulling the instance out of the load
balancer will not make the index any more current. The status endpoint reports the
lag instead.

### Search

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/search/products` | search products by text |
| `GET` | `/api/v1/search/status` | index progress |

### Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `q` | - | **required**, the search term |
| `category_id` | - | filter by category |
| `status` | - | `draft`, `active`, `archived` |
| `min_price_cents` | - | lower price bound |
| `max_price_cents` | - | upper price bound |
| `limit` | `20` | max `100` |
| `offset` | `0` | for paging |

```bash
curl 'localhost:8084/api/v1/search/products?q=aurora&limit=10'
```

```json
{
  "products": [
    {
      "id": 4, "category_id": 1, "name": "Nimbus Laptop", "slug": "nimbus-laptop",
      "brand": "Aurora", "price_cents": 4000, "currency": "IDR", "stock": 9,
      "status": "active", "score": 1.0, "created_at": "2026-09-30T12:00:00Z"
    }
  ],
  "total": 4, "limit": 10, "offset": 0, "took_ms": 8
}
```

`status` that is not sent means **no filter**, not `draft`. Applying `draft` to a
request that did not ask for it would hide every active product and answer a
request for the whole catalogue with drafts only.

`score` is only comparable between hits of one response, and it is engine-specific.
Do not compare it across queries, and do not cache it.

### The term must contain three consecutive letters or digits

A trigram is three characters, and `pg_trgm` only extracts trigrams from runs of
alphanumerics. A shorter run yields none, and a pattern with none is handed to the
index as "consider everything", which degrades into a full index scan. Verified
against PostgreSQL 17: a two character needle produced a `Seq Scan` while a three
character needle produced a `Bitmap Index Scan` on the same table and index.

So the floor is **per run, not per term**, which is the part that is easy to get
wrong. `"a b c"` is five characters long and has no trigram anywhere in it, and is
correctly refused. A shopper typing `%` gets a 400 explaining the constraint rather
than a sequential scan of the catalogue.

```bash
curl 'localhost:8084/api/v1/search/products?q=ab'    # 400, explains the floor
curl 'localhost:8084/api/v1/search/products?q=50%25%20off'  # fine, finds "50% Off"
```

### Wildcards are literal

`%` and `_` in a term are escaped, so a shopper searching `50% off` means a percent
sign and not "match everything". Verified against PostgreSQL 17: escaped, the term
matched only the rows that contain a literal percent sign; unescaped, a bare `%`
matched every row in the table.

The term is lowercased before it is compared, because the indexed column is
lowercased and `LIKE` is case-sensitive. Lowering only the column looks right and
is not: verified against PostgreSQL 17, `'%AURORA%'` against a lowercased column
returned 0 rows where `'%aurora%'` returned 2.

### Status

```bash
curl localhost:8084/api/v1/search/status
```

```json
{"documents": 128, "applied_events": 402, "healthy": true}
```

`documents: 0` with `healthy: true` means the consumer has not caught up or has
never run, not that the catalogue is genuinely empty - and that is why this
endpoint exists.

### gRPC

`productsearch.v1.ProductSearchService` on port `9094`:

| RPC | Description |
|-----|-------------|
| `SearchProducts` | search products by text, ranked |

```bash
grpcurl -plaintext localhost:9094 list
grpcurl -plaintext -d '{"query":"aurora","limit":5}' \
  localhost:9094 productsearch.v1.ProductSearchService/SearchProducts
```

### Status codes

| Code | When |
|------|------|
| `200` | search succeeded, possibly with no hits |
| `400` | missing term, term below the trigram floor, or an invalid filter |
| `405` | a mutating route was called; writes go to product-service |
| `500` | internal failure, detail only in the log |

## Database

`product_search` is the read model. Its primary key is the id assigned by
product-service, so the projection never maps one id space onto another.

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- trusted, so the DB owner suffices

CREATE TABLE IF NOT EXISTS product_search (
    product_id   BIGINT PRIMARY KEY,
    category_id  BIGINT NOT NULL,
    name         VARCHAR(255) NOT NULL,
    slug         VARCHAR(255) NOT NULL,
    description  TEXT,
    brand        VARCHAR(255),
    price_cents  BIGINT NOT NULL,
    currency     CHAR(3) NOT NULL,
    stock        INTEGER NOT NULL DEFAULT 0,
    status       VARCHAR(16) NOT NULL,
    primary_media_id BIGINT,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_id UUID NOT NULL,

    search_text  TEXT GENERATED ALWAYS AS (
        lower(coalesce(name,'') || ' ' || coalesce(brand,'') || ' ' || coalesce(slug,''))
    ) STORED,
    ...
);

CREATE INDEX IF NOT EXISTS idx_product_search_text
    ON product_search USING GIN (search_text gin_trgm_ops);
```

### Why one combined column

Indexing `name`, `brand` and `slug` separately means three GIN indexes to maintain
on every product event, and the planner combines per-column `OR` branches with a
bitmap only if *every* branch is indexable - one non-indexable branch collapses the
whole plan to a sequential scan. One combined column is one index, one bitmap, and
no such cliff.

It is a stored **generated column** rather than an expression index, because the
query then reads `search_text LIKE ...` with no obligation to reproduce the
expression exactly. A mismatch there fails silently as a sequential scan, with no
error to notice.

`description` is stored but deliberately **not** indexed. A trigram index costs
roughly one entry per trigram of indexed text, so long marketing copy would
dominate the index while adding little to what a shopper types. This matches what
product-listing-service searched before the split. Widening it is a one-line change
here, and for long text the right tool is a `tsvector` GIN index rather than a
trigram one.

### Why the ranking uses `similarity`, not `word_similarity`

`word_similarity` looks like the better fit and in isolation it is, but the filter
has already selected documents that contain the term contiguously, so every
surviving hit holds the term's trigrams in one extent and `word_similarity` returns
exactly `1.0` for all of them. It cannot rank anything.

`similarity` measures how much of the document the term accounts for, so it does
separate them. Measured against PostgreSQL 17 on three documents that all contain
`aurora`:

| document | `word_similarity` | `similarity` |
|----------|-------------------|--------------|
| `Aurora` | 1.00 | 1.00 |
| `Auroraphone` | 0.86 | 0.46 |
| `Aurora Phone 15 Pro Max Ultra Limited Edition Bundle` | 1.00 | 0.14 |

`word_similarity` ties the exact match with the long title and leaves their order to
the tiebreaker. `similarity` separates all three, putting the product whose title
really is about the query first.

`GREATEST` takes the best single field rather than averaging, so a term that is the
product's whole brand outranks one that is a fragment of a long title. That is the
shape Elasticsearch calls `multi_match` with `best_fields`, and it is why the
arithmetic is kept separate from the indexable text filter: the index narrows the
candidates, and only the candidates are scored.

No `lower()` appears in the ranking expression. `pg_trgm` lowercases both sides
before extracting trigrams, so `similarity` is already case-insensitive on the raw
column.

### The pending list is left at its default

`gin_pending_list_limit` looks like a tuning knob worth raising - fewer foreground
flushes means less latency variance - and it was tried here at 64MB. It made search
dramatically worse, measurably so.

GIN writes accumulate in a pending list that every search must walk linearly until
autovacuum flushes it. With a 64MB limit the list stayed unflushed: after loading
60k products the index was 31MB, a `Bitmap Index Scan` was costed at **17026**
against 2465 for reading the entire 13MB table, and the planner correctly preferred
a sequential scan - so the index was never used at all. After one `VACUUM` flushed
the list, the same query cost **38.89** and the index won.

A larger pending list therefore does not only delay the flush, it raises the cost of
every search in the meantime. The default bounds that cost, which is the quantity
that matters on the request path.

### Watch the pending list in production

The same mechanism applies to a live catalogue. If search latency climbs while the
index size looks larger than the data, check whether the pending list is backing up
and whether autovacuum is keeping up with the projection's write rate:

```sql
SELECT * FROM gin_stat_pending_list('idx_product_search_text'::regclass);
SELECT indexrelname, idx_scan, idx_tup_read FROM pg_stat_user_indexes
  WHERE relname = 'product_search';
```

### processed_events

```sql
CREATE TABLE IF NOT EXISTS processed_events (
    event_id      UUID PRIMARY KEY,
    event_type    TEXT NOT NULL,
    aggregate_id  BIGINT NOT NULL,
    topic         TEXT NOT NULL,
    partition     INTEGER NOT NULL,
    kafka_offset  BIGINT NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

This is the idempotency gate. `event_id` is a UUID unique per event, so a
redelivery is detected exactly, and the check plus the insert is one statement. Rows
are pruned by `PROCESSED_EVENT_RETENTION` (default 24h), because a redelivery
arrives within seconds and the broker will not replay that offset again once the
window has passed.

`consumer_checkpoints` records the applied offset per partition. That is operator
bookkeeping, not a source of truth: the consumer group holds the authoritative
offset in Kafka. The table answers "how far behind is the index", which the broker
cannot.

## Configuration

Every variable is optional; development-friendly defaults let the service start with
no configuration at all.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `search_user` | database user |
| `DB_PASSWORD` | `search_pass` | database password |
| `DB_NAME` | `search_db` | database name |
| `DB_SSLMODE` | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `DB_MAX_OPEN_CONNS` | `25` | connection pool bound |
| `DB_MAX_IDLE_CONNS` | `10` | idle pool, may not exceed max open |
| `DB_CONN_MAX_LIFETIME` | `5m` | connection recycling |
| `DB_CONNECT_TIMEOUT` | `10s` | initial connect and ping |
| `KAFKA_BROKERS` | `localhost:9092` | seed broker list, `host:port` comma separated |
| `KAFKA_CLIENT_ID` | `product-search-service` | identity in broker logs |
| `KAFKA_CONSUMER_GROUP` | `product-search-service` | every replica joins this group |
| `KAFKA_TOPIC` | `product.events.v1` | topic of product events |
| `KAFKA_DEAD_LETTER_TOPIC` | `product.events.v1.dlq` | topic for records that cannot be applied |
| `KAFKA_MAX_POLL_RECORDS` | `200` | one fetch bound |
| `KAFKA_POLL_TIMEOUT` | `1s` | one poll wait bound |
| `KAFKA_RETRY_BACKOFF` | `1s` | pause after a transient failure |
| `CONSUMER_ENABLED` | `true` | set false to run the API without consuming |
| `PROCESSED_EVENT_RETENTION` | `24h` | how long a processed-event id is kept |
| `PRUNE_INTERVAL` | `1h` | how often the prune runs |
| `APP_PORT` | `8084` | REST listen port |
| `GRPC_PORT` | `9094` | gRPC listen port |
| `GRPC_MAX_MESSAGE_SIZE` | `16777216` | one gRPC message bound |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | slowloris protection |
| `HTTP_READ_TIMEOUT` | `15s` | request read bound |
| `HTTP_WRITE_TIMEOUT` | `15s` | response write bound |
| `HTTP_IDLE_TIMEOUT` | `60s` | keep-alive bound |
| `SHUTDOWN_TIMEOUT` | `30s` | drain window on graceful shutdown |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |

`KAFKA_DEAD_LETTER_TOPIC` is required while the consumer is enabled, and must differ
from `KAFKA_TOPIC`. Without it one poisoned record holds the partition forever;
putting it on the same topic means the consumer re-reads the record it just rejected.

`KAFKA_CONSUMER_GROUP` must not be `product-listing-service`. The two services read
the same topic into different indexes, and a shared group would split the partitions
between them so each indexed only part of the catalogue.

Database credentials never reach the log: what is recorded is `RedactedDSN()`.

> **Security note.** The Kafka connection uses plaintext transport, assuming traffic
> stays inside the cluster network. For a cross-network deployment add TLS and SASL
> to `kgo.NewClient(...)` in `internal/messaging/consumer.go`.

## Development

```bash
go run ./cmd/server
go test ./...
go test -race ./...
go test -cover ./...
```

Tests need no database, Kafka or other service: `search.Engine` and
`projection.Writer` are interfaces, so both are tested with stubs.
`internal/event` is tested against envelopes in their real shape, so a contract
change fails a test rather than surfacing quietly in the consumer.

### Tests that need PostgreSQL

The index is a projection built on trigram indexes, and most of what matters about
it is not visible in the query strings. `internal/repository/engine_sql_test.go`
verifies against a real PostgreSQL 17:

- A term matches **literally**, including `%`, `_` and `\`, through the real service
  so the lowercasing is exercised rather than hidden
- Metacharacter-only terms are refused by the trigram floor
- **Ranking is real**: the exact title outranks the fragment, which outranks the term
  buried in a long title, with the measured gaps asserted
- A rename **reaches `search_text`**, so the old name stops being findable. An
  upsert that updated the row but not the indexed text would be a stale index rather
  than a stale read model
- The upsert is **idempotent**, and a redelivery changes nothing
- The dedup gate is one statement, and `SetStock` leaves other columns alone
- A removed product **stops matching**
- The **trigram index is reached by the planner** on the real query shape, and the
  ranked variant too. The corpus of non-matching rows is what makes the assertion
  meaningful; on a small table PostgreSQL prefers a sequential scan whatever indexes
  exist
- A **two character pattern cannot use the index** and a four character rare one
  can, which is what justifies refusing short terms

That last group needs one detail that looks like an oversight and is not: the test
runs `VACUUM ANALYZE` before asserting on a plan. GIN writes sit in a pending list
that every search walks linearly, so without the flush the assertion measures the
pending list rather than the index. The numbers behind that are in the migration
comment above. Autovacuum does it in production; the test does it explicitly so the
assertion is about the steady state.

Tests skip themselves when `POSTGRES_TEST_DSN` is unset, so `go test ./...` still
runs with no database:

```bash
docker run -d --name pgtest -p 55432:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine
docker exec pgtest psql -U postgres -q -c "CREATE DATABASE search_db"
docker exec -i pgtest psql -U postgres -q -d search_db < internal/database/migrations/0001_init.sql

POSTGRES_TEST_DSN="postgres://postgres:pw@127.0.0.1:55432/search_db?sslmode=disable" \
  go test ./internal/repository/
```

`pg_trgm` ships in the contrib modules the official `postgres:17-alpine` image
already builds, so no custom image is needed.

Tests `TRUNCATE` the tables they use, so point them at a dedicated database rather
than the one the stack is using.

To run the search API without a consumer, for instance while rebuilding the index:

```bash
CONSUMER_ENABLED=false go run ./cmd/server
```

### Regenerating the gRPC stubs

Stubs in `gen/` are committed. After changing a `.proto`:

```bash
./hack/gen-proto.sh
```

## Known limitations

- **No typo tolerance.** pg_trgm has it for free, but the indexable form is
  `column %> needle` - the needle is the *second* argument, the reverse of the
  `word_similarity(term, column)` function, and it reads a GUC
  (`pg_trgm.word_similarity_threshold`) that is not settable before the extension's
  library is loaded in a session. Exposing a per-request threshold on a pooled
  connection is a footgun, so it is left for a dedicated engine. Both facts are
  verified against PostgreSQL 17.
- **Offset paging.** `OFFSET` re-sorts everything skipped, so deep pages get slower.
  Keyset paging on `(score, product_id)` is the fix and is portable to
  `search_after`, but the API is `limit`/`offset` today because that is what the
  listing service exposes.
- **No facets.** See above.
- **No highlighting.** pg_trgm has no equivalent of `ts_headline`; the application
  would have to mark substrings itself.
- **`description` is not searchable.** See the schema section.

## License

MIT
