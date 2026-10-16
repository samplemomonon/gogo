# Product Service

**Write side of the product domain.** REST + gRPC service that accepts commands,
validates them, stores them, and publishes the resulting domain events to Kafka.

This service serves no `GET` routes. Reading products is
[product-listing-service](../product-listing-service/)'s job: it consumes the
events published here and maintains its own read model, so a catalogue browse
never competes with writes for the write path's indexes and the two can be scaled
and deployed independently.

The two are joined by Kafka, not by a call. The consequence worth knowing: a read
can briefly disagree with a write that just returned `201`, because the event has
committed but the projection has not caught up. That is the trade for write
throughput and for removing a synchronous dependency from the read path.

## Features

- **Transactional outbox** - every change is committed together with the event
  describing it, so an event can never be lost
- **Idempotent producer** - a retried publish cannot duplicate a record in the log
- **Notify-driven relay** - events publish as soon as they commit, with a poll as
  the safety net
- **Bounded retries with a dead end** - an undeliverable event is marked failed
  and logged rather than retried forever or silently dropped
- **Validasi lintas service via gRPC** - category and media are checked before
  anything is written
- **Koneksi lazy** - the service starts even when Kafka and the other services
  are not up yet
- **`503` saat dependency mati** - retry, bukan request yang salah
- **Money as minor units** - `price_cents` `BIGINT`, tidak pernah float
- **Slug unik** - diturunkan dari nama bila tidak diisi
- **Variants** - SKU unik, cascade delete dari product
- **Partial update** - hanya field yang dikirim yang berubah
- **Stock adjustment atomik** - `UPDATE ... SET stock = stock + $2`
- **Graceful shutdown** - REST, gRPC, and the relay

## Architecture

```
product-service/
├── cmd/
│   └── server/
│       └── main.go                     # entrypoint, wiring, lifecycle
├── internal/
│   ├── apperror/
│   │   └── apperror.go                 # sentinel error domain
│   ├── config/
│   │   └── config.go                   # env config + validasi
│   ├── database/
│   │   ├── database.go                 # connection pool, migrator
│   │   └── migrations/
│   │       ├── 0001_init.sql           # products, variants, trigger
│   │       └── 0002_outbox.sql         # outbox table, index, NOTIFY trigger
│   ├── event/
│   │   └── event.go                    # the published contract: envelope,
│   │                                   # event types, payload snapshots
│   ├── grpcclient/
│   │   └── clients.go                  # client category + media
│   ├── grpcsrv/
│   │   └── product_server.go           # adapter gRPC -> service
│   ├── handler/
│   │   ├── product_handler.go          # HTTP handlers (writes only)
│   │   ├── health_handler.go           # liveness, readiness, version
│   │   └── middleware.go               # logging, recovery, headers
│   ├── httpx/
│   │   └── httpx.go                    # JSON helpers, error mapping
│   ├── messaging/
│   │   └── producer.go                 # franz-go producer
│   ├── model/
│   │   └── product.go                  # domain types
│   ├── outbox/
│   │   ├── outbox.go                   # claim, mark, reschedule, stats
│   │   ├── recorder.go                 # derived events, post-commit
│   │   └── relay.go                    # the background publisher
│   ├── repository/
│   │   ├── event_sink.go               # EventSink + EventRecorder contracts
│   │   ├── product_repository.go       # SQL products
│   │   └── variant_repository.go       # SQL variants
│   ├── service/
│   │   ├── domain_events.go            # stored row -> domain events
│   │   └── product_service.go          # business rules
│   └── version/
│       └── version.go                  # build metadata
├── gen/                                # generated stubs, di-commit
│   ├── categorypb/                     # salinan dari category-service
│   ├── mediapb/                        # salinan dari media-service
│   └── productpb/
├── hack/
│   └── gen-proto.sh                    # regenerasi stub
├── Dockerfile
└── docker-compose.yml
```

Aliran dependency satu arah: `handler -> service -> repository -> database`, dan
`grpcclient -> service`. `service` mendefinisikan interface `CategoryLookup` dan
`MediaLookup`, jadi test tidak perlu menjalankan service lain.

Stub `categorypb` dan `mediapb` adalah salinan dari file `.proto` service
pemiliknya, dengan hanya `go_package` yang diubah agar milik modul ini.

## Why an outbox

Publishing to Kafka directly from the request path means writing to two systems
that cannot commit together. If the process dies after the product row commits but
before the produce call returns, the event is gone and no read model will ever
learn about the product. Publishing first has the mirror problem: an event for a
change that then rolled back.

The outbox closes that window by making the event part of the transaction that
already exists:

```
BEGIN
  INSERT INTO products ... RETURNING ...
  INSERT INTO outbox_events ...        -- same transaction
COMMIT
```

Either both commit or neither does. A relay then moves the rows to Kafka, claiming
batches with `FOR UPDATE SKIP LOCKED` so several replicas can share the table
without a lock manager. The claim is a single statement, so row locks are released
before anything is sent to the broker; a short lease covers the gap.

The guarantee is **at-least-once**, not exactly-once. A relay that publishes and
then crashes before recording the fact will publish again, so consumers dedupe on
the event id. That is the cheaper half of the problem to get right: duplication is
handled by a key the consumer already stores, whereas a silently lost event has no
recovery path short of a full database scan.

### Event ordering

Every event about a product - including the ones about its variants - is keyed by
the product id. Kafka only guarantees order within a partition, so keying by
aggregate is what guarantees `product.created` is always seen before the
`product.updated` that follows it, while unrelated products still spread across
partitions and consume them in parallel.

A variant event names the variant as its aggregate, so a consumer can tell what it
is about, but its *key* is the parent product. Keying by the variant would put it
on another partition where no ordering is promised, and a consumer could then see
a product snapshot republished after a variant was added before it saw the variant
event that snapshot already accounts for.

### Events are built from the committed row

The repository builds each event from the row it just wrote, inside the
transaction, via the `EventRecorder` interface. The service does not assemble
events and pass them in. That is not a style preference: a product's id, its
timestamps and its variants' ids are all assigned by the database, so an event
assembled beforehand would publish an aggregate that does not exist yet.

The one exception is documented in `internal/outbox/recorder.go`: republishing a
product after a variant change cannot join the variant's transaction, because it
needs the product row the variant insert did not touch.

## Quick start

Product-service publishes to Kafka, so the broker has to be reachable. Jalankan
seluruh stack dari root repository:

```bash
docker compose -f ../docker-compose.yml up -d --build
```

Untuk menjalankan stack secara terpisah, arahkan target Kafka ke host:

```bash
KAFKA_BROKERS=host.docker.internal:9092 \
CATEGORY_GRPC_TARGET=host.docker.internal:9090 \
MEDIA_GRPC_TARGET=host.docker.internal:9091 \
docker compose up -d --build
```

## API

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health`, `/healthz` | liveness, tidak bergantung dependency |
| `GET` | `/readyz` | readiness, ping database dengan timeout 3s |
| `GET` | `/version` | versi dan build info |

`/readyz` sengaja **tidak** memeriksa Kafka. Broker yang mati hanya menunda
read model menyusul; request yang masuk tetap masuk ke outbox dan dipublikasikan
begitu broker kembali. Mengambil service ini dari load balancer justru akan
memperburuk backlog.

### Products

Semua route bermutasi. Tidak ada `GET`; baca dari product-listing-service.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/products` | buat produk, opsional dengan variants |
| `PUT` | `/api/v1/products/{id}` | update sebagian field |
| `DELETE` | `/api/v1/products/{id}` | hapus produk dan variants, `204` |
| `POST` | `/api/v1/products/{id}/stock` | adjust stok dengan `delta` |

### Variants

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/products/{id}/variants` | tambah variant |
| `PUT` | `/api/v1/products/{id}/variants/{vid}` | update variant |
| `DELETE` | `/api/v1/products/{id}/variants/{vid}` | hapus variant, `204` |

### Contoh

```bash
curl -X POST localhost:8082/api/v1/products \
  -H 'Content-Type: application/json' \
  -d '{
    "category_id": 1,
    "name": "Aurora Phone 15 Pro",
    "description": "6.7 inch OLED",
    "brand": "Aurora",
    "price_cents": 12999000,
    "stock": 25,
    "status": "active",
    "variants": [
      {"sku": "aurora-15-pro-256-blk", "name": "256GB Black", "price_cents": 12999000, "stock": 10},
      {"sku": "aurora-15-pro-512-blu", "name": "512GB Blue",  "price_cents": 14999000, "stock": 15}
    ]
  }'
```

`slug` (`aurora-phone-15-pro`), `currency` (`IDR`) dan `status` (`draft`) diisi
otomatis. SKU dinormalisasi ke huruf besar, dan variant mewarisi `currency`
produknya kecuali punya sendiri.

### Partial update

`PUT` hanya mengubah field yang dikirim. `null` berarti kosongkan field:

```bash
# hanya ganti nama
curl -X PUT localhost:8082/api/v1/products/1 \
  -H 'Content-Type: application/json' -d '{"name":"Aurora Phone 16"}'

# kosongkan brand
curl -X PUT localhost:8082/api/v1/products/1 \
  -H 'Content-Type: application/json' -d '{"brand":null}'

# lepaskan gambar, tanpa memanggil media-service
curl -X PUT localhost:8082/api/v1/products/1 \
  -H 'Content-Type: application/json' -d '{"primary_media_id":null}'
```

Body kosong `{}` ditolak dengan `400`, karena tidak mengubah apa pun dan
biasanya berarti bug di sisi client.

### Stock

```bash
# jual 3 unit
curl -X POST localhost:8082/api/v1/products/1/stock \
  -H 'Content-Type: application/json' -d '{"delta":-3}'

# restock
curl -X POST localhost:8082/api/v1/products/1/stock \
  -H 'Content-Type: application/json' -d '{"delta":50}'
```

Penyesuaian yang akan membuat stok negatif ditolak `400`. `delta: 0` ditolak
karena tidak melakukan apa pun.

### gRPC

`product.v1.ProductService` pada port `9092`:

| RPC | Description |
|-----|-------------|
| `CreateProduct` | validasi category dan media via gRPC, lalu simpan |
| `UpdateProduct` | partial update, `optional` fields |
| `DeleteProduct` | hapus produk |

```bash
grpcurl -plaintext localhost:9192 list
grpcurl -plaintext -d '{"id":1}' \
  localhost:9192 product.v1.ProductService/CreateProduct
```

`UpdateProduct` memakai proto3 `optional`, jadi field yang tidak dikirim tidak
menimpa kolom yang ada - berbeda dari proto3 biasa di mana `0` tidak bisa
dibedakan dari "tidak dikirim".

> Di dalam jaringan compose port gRPC tetap `9092`. Yang dipetakan ke `9192`
> hanya port host, karena `9092` sudah dipakai Kafka.

### Status codes

| Code | Kapan |
|------|-------|
| `200` | sukses update |
| `201` | create, dengan header `Location` |
| `204` | delete, tanpa body |
| `400` | input tidak valid, category/media tidak ada, stok jadi negatif |
| `404` | produk atau variant tidak ada |
| `405` | route read dipanggil di service ini; baca dari product-listing-service |
| `409` | slug atau SKU bentrok |
| `503` | category-service atau media-service tidak bisa dihubungi |
| `500` | kegagalan internal, detail hanya di log |

## Database

Selain `products` dan `product_variants` (lihat `0001_init.sql`), tabel
`outbox_events`:

```sql
CREATE TABLE IF NOT EXISTS outbox_events (
    id             BIGSERIAL PRIMARY KEY,
    event_id       UUID        NOT NULL UNIQUE,
    topic          TEXT        NOT NULL,
    event_type     TEXT        NOT NULL,
    aggregate_type TEXT        NOT NULL,
    aggregate_id   BIGINT      NOT NULL,
    partition_key  TEXT        NOT NULL,
    payload        JSONB       NOT NULL,

    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at    TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ,

    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    leased_until    TIMESTAMPTZ,
    last_error      TEXT,

    CONSTRAINT outbox_events_attempts_non_negative CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL AND failed_at IS NULL;
```

`event_id` dibuat oleh database, jadi tidak ada dependensi uuid di Go, dan relay
serta consumer pasti sepakat pada satu nilai.

Index-nya partial di atas event yang belum dipublikasikan. Itu jalur panas relay,
dan relay hanya pernah melihat pekerjaan yang tertunda, sehingga mengindeks
seluruh riwayat membuat query itu tidak lebih cepat.

Baris yang sudah `published_at` tidak dihapus. `outbox_events` adalah jejak audit:
`failed_at IS NOT NULL` berarti ada perubahan yang sudah commit yang tidak pernah
mencapai read model, dan itu justru yang perlu diketahui operator.

## Events

Topik `product.events.v1`. Envelope:

```json
{
  "event_id": "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
  "event_type": "product.v1.product.created",
  "schema_version": 1,
  "aggregate_type": "product",
  "aggregate_id": 42,
  "occurred_at": "2026-09-30T12:00:00Z",
  "payload": { }
}
```

`event_type`, `event_id`, `aggregate_type` dan `schema_version` juga dikirim
sebagai Kafka headers, jadi consumer bisa me-routing tanpa decode body.

| Event | Dipublikasikan saat |
|-------|---------------------|
| `product.v1.product.created` | produk dibuat, satu per variant langsung setelah |
| `product.v1.product.updated` | produk di-update, dan setelah variant berubah |
| `product.v1.product.deleted` | produk dihapus |
| `product.v1.product.stock_adjusted` | stok berubah |
| `product.v1.variant.created` | variant dibuat |
| `product.v1.variant.updated` | variant di-update |
| `product.v1.variant.deleted` | variant dihapus |

Setiap event membawa **snapshot penuh** dari aggregate, bukan diff. Snapshot
mandiri sehingga consumer tidak perlu membaca database producer untuk
memahaminya, dan menerapkan event yang sama dua kali menghasilkan state yang sama.
Diff lebih kecil, tapi membuat consumer bergantung pada sudah melihat setiap event
sebelumnya dalam urutan yang benar - persis sifat yang tidak dijamin transport
at-least-once.

`stock_adjusted` membawa hasil akhirnya, bukan delta, karena delta menuntut
consumer sudah melihat setiap penyesuaian sebelumnya.

## Configuration

Semua variabel opsional; default pengembangan tersedia agar service bisa start
tanpa konfigurasi.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `product_user` | database user |
| `DB_PASSWORD` | `product_pass` | database password |
| `DB_NAME` | `product_db` | database name |
| `DB_SSLMODE` | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `DB_MAX_OPEN_CONNS` | `25` | batas connection pool |
| `DB_MAX_IDLE_CONNS` | `10` | idle pool, tidak boleh melebihi max open |
| `DB_CONN_MAX_LIFETIME` | `5m` | daur ulang koneksi |
| `DB_CONNECT_TIMEOUT` | `10s` | timeout koneksi awal |
| `CATEGORY_GRPC_TARGET` | `localhost:9090` | host:port category-service gRPC |
| `MEDIA_GRPC_TARGET` | `localhost:9091` | host:port media-service gRPC |
| `DEPENDENCY_TIMEOUT` | `3s` | batas satu panggilan gRPC keluar |
| `DEFAULT_CURRENCY` | `IDR` | ISO 4217 code saat request tidak menyebut currency |
| `KAFKA_BROKERS` | `localhost:9092` | seed broker list, host:port dipisah koma |
| `KAFKA_CLIENT_ID` | `product-service` | identitas producer di log broker |
| `KAFKA_PUBLISH_TIMEOUT` | `10s` | batas satu panggilan publish |
| `OUTBOX_BATCH_SIZE` | `100` | event per claim relay |
| `OUTBOX_POLL_INTERVAL` | `5s` | sweep cadangan; NOTIFY yang memangkas latency |
| `OUTBOX_LEASE` | `30s` | berapa lama satu claim dipegang relay |
| `OUTBOX_MAX_ATTEMPTS` | `10` | percobaan sebelum event ditandai failed |
| `APP_PORT` | `8082` | listen port REST |
| `GRPC_PORT` | `9092` | listen port gRPC |
| `GRPC_MAX_MESSAGE_SIZE` | `16777216` | batas satu pesan gRPC |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | proteksi slowloris |
| `HTTP_READ_TIMEOUT` | `15s` | batas baca request |
| `HTTP_WRITE_TIMEOUT` | `15s` | batas tulis response |
| `HTTP_IDLE_TIMEOUT` | `60s` | batas keep-alive |
| `SHUTDOWN_TIMEOUT` | `30s` | drain window saat graceful shutdown |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` atau `text` |

Kredensial database tidak pernah masuk log: yang dicatat adalah
`RedactedDSN()`. Target gRPC dan Kafka aman dicatat karena tidak membawa
kredensial.

> **Catatan keamanan.** Koneksi gRPC dan Kafka memakai transport plaintext, dengan
> asumsi trafik hanya berjalan di jaringan cluster. Untuk deployment lintas
> jaringan, ganti `insecure.NewCredentials()` di
> `internal/grpcclient/clients.go` dan tambahkan kredensial TLS di
> `internal/messaging/producer.go`.

## Development

```bash
go run ./cmd/server
go test ./...
go test -race ./...
go test -cover ./...
```

`go run` tanpa dependency akan tetap start; `/readyz` menjawab ready selama
database hidup, dan request yang butuh referensi lintas service akan `503`.

Test tidak butuh database, Kafka, atau service lain: layer `service` mendefinisikan
interface untuk `repository` dan untuk lookups, jadi semuanya diuji dengan stub.
`internal/outbox` diuji dengan repository dan publisher rekaman, sehingga loop
relay - termasuk backoff, backoff exhaustion, dan isolasi event yang gagal - bisa
diuji tanpa broker.

### Test yang butuh PostgreSQL

Query claim adalah statement paling halus di service ini, dan propertinya tidak
terlihat dari membacanya. `internal/outbox/claim_sql_test.go` membawanya ke
PostgreSQL sungguhan untuk memeriksa dua hal yang menopang seluruh desain
konkurensinya:

- Claim adalah **satu statement**, jadi lock baris yang diambilnya dilepas begitu
  statement selesai. Tidak ada yang dipegang lintas pemanggilan jaringan ke Kafka.
- `FOR UPDATE SKIP LOCKED` membuat relay kedua **melewati** baris yang terkunci
  alih-alih menunggu, yang memungkinkan beberapa relay berbagi tabel. Query yang
  sama tanpa `SKIP LOCKED` memblokir selama seluruh durasi transaksi relay lain.

Test ini skip sendiri kalau `POSTGRES_TEST_DSN` tidak diisi, jadi `go test ./...`
tetap jalan tanpa database:

```bash
docker run -d --name pgtest -p 55432:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine
docker exec pgtest psql -U postgres -q -c "CREATE DATABASE product_db"
docker exec -i pgtest psql -U postgres -q -d product_db < internal/database/migrations/0002_outbox.sql

POSTGRES_TEST_DSN="postgres://postgres:pw@127.0.0.1:55432/product_db?sslmode=disable" \
  go test ./internal/outbox/
```

Test mem-`TRUNCATE` `outbox_events`, jadi arahkan ke database khusus, bukan ke
database yang sedang dipakai stack.

### Regenerasi stub gRPC

Stub di `gen/` di-commit. Setelah mengubah `.proto` di service mana pun:

```bash
# di service yang memiliki .proto
./hack/gen-proto.sh

# salin stub ke product-service
cp ../category-service/proto/category/v1/category.proto \
   proto/category/v1/category.proto   # lalu sesuaikan go_package
./hack/gen-proto.sh
```

## License

MIT
