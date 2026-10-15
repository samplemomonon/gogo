# Product Listing Service

**Browse side of the product domain.** REST + gRPC service that serves the product
catalogue from its own database, and keeps that database current by consuming the
domain events [product-service](../product-service/) publishes.

It answers *browsing*: what is in this category, what is active, what is in this
price band. Free-text search used to be the `q` parameter here and now lives in
[product-search-service](../product-search-service/), because the two workloads have
different costs - a browse table wants cheap indexes on structured columns, a search
index wants a trigram index plus a relevance sort. See that service for the
rationale.

This service holds no authoritative data. Every row is a projection of an event,
and the whole read model can be rebuilt at any time by replaying the topic:

```bash
docker compose stop product-listing-app
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server kafka:19092 --delete --group product-listing-service
docker compose exec product-listing-postgres psql -U listing -d listing_db \
  -c "truncate processed_events, product_listings, product_listing_variants"
docker compose start product-listing-app
```

That is the property a separate read service buys: the read schema can be
reshaped, reindexed or rebuilt without touching the write path, and the write path
never has to maintain an index it does not use.

## Features

- **Consumes product events from Kafka** - no synchronous call to the write side
- **Idempotent projection** - a redelivered event changes nothing
- **Manual offset commits** - offsets advance only past events that were applied
- **Dead-letter topic** - a record that can never be applied does not block its
  partition
- **Rebuildable from the log** - the read model is disposable by design
- **Denormalised for browsing** - indexes shaped for reads, not for writes
- **No search** - `q` is refused with a pointer to product-search-service
- **`projected_at` on every row** - a caller can tell fresh data from a stale one
- **Projection status endpoint** - because an empty read model looks exactly like
  a healthy one from the outside
- **Graceful shutdown** - HTTP, gRPC, consumer, pruner

## Architecture

```
product-listing-service/
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
│   │       ├── 0001_init.sql           # read model, processed events, checkpoints
│   │       └── 0002_drop_search_index.sql  # search moved out
│   ├── event/
│   │   └── event.go                    # decoder for the published contract
│   ├── grpcsrv/
│   │   └── listing_server.go           # adapter gRPC -> service
│   ├── handler/
│   │   ├── health_handler.go           # liveness, readiness, version
│   │   ├── listing_handler.go          # HTTP read handlers
│   │   ├── middleware.go               # logging, recovery, headers
│   │   └── status_handler.go           # projection progress
│   ├── httpx/
│   │   └── httpx.go                    # JSON helpers, error mapping
│   ├── messaging/
│   │   ├── consumer.go                 # franz-go group consumer
│   │   └── publisher.go                # dead-letter producer
│   ├── model/
│   │   └── product.go                  # read model types
│   ├── projection/
│   │   └── projection.go               # event -> read model
│   ├── repository/
│   │   ├── listing_repository.go       # read queries
│   │   ├── projector.go                # upserts, dedup, checkpoints
│   │   └── variant_scan.go             # variant row scanning
│   ├── service/
│   │   └── listing_service.go          # read rules
│   └── version/
│       └── version.go                  # build metadata
├── gen/listingpb/                      # generated stubs, di-commit
├── proto/listing/v1/listing.proto
├── hack/
│   └── gen-proto.sh                    # regenerasi stub
├── Dockerfile
└── docker-compose.yml
```

Aliran dependency satu arah: `handler -> service -> repository -> database`, dan
`consumer -> projection -> repository`. `projection` adalah bagian yang halus,
jadi ia diuji tanpa broker lewat `Writer` interface.

`internal/event` mendeklarasikan sendiri kontrak yang dimiliki product-service.
Berbagi tipe Go akan berarti sebuah dependency antar modul, yang mengunci urutan
deploy kedua service; struct yang diduplikasi lebih murah, dan ketidakcocokan
tampil sebagai decode error di consumer, bukan compile error.

## Delivery semantics

Consumer memakai commit manual dan deduplikasi berbasis event id, sehingga
kombinasi ini menghasilkan **at-least-once delivery dengan efek exactly-once pada
read model**:

- Offset di-commit **setelah** event berhasil diterapkan. Kalau commit gagal,
  batch di-read ulang dan idempotensi projector mengubahnya jadi no-op.
- Kalau satu record gagal secara transient, offset **tidak boleh** melewati record
  itu. Hanya prefix yang sudah ditangani yang di-commit. Ini detail yang mudah
  salah: meng-commit seluruh batch akan bergerak melewati event yang read model
  tidak pernah lihat, dan event itu hilang permanen.
- Rejektor selalu deduplikasi lebih dulu, jadi redelivery hanya membutuhkan satu
  lookup terindeks dan tanpa write.
- Tipe event yang tidak dikenal di-skip **tanpa** ditandai processed. Producer
  yang lebih baru boleh saja mengirim event yang belum dipahami versi ini, dan
  menandainya processed akan membuangnya secara diam-diam.

### Dead letter

Record yang tidak bisa pernah diterapkan - envelope rusak, versi schema yang tidak
cocok - dikirim ke `product.events.v1.dlq` lengkap dengan posisi aslinya sebagai
header, lalu offset-nya di-commit. Tanpa itu, satu record beracun akan menahan
partisi itu selamanya dan semua produk di depannya tidak akan pernah terbaca.

Record yang gagal secara transient **tidak** masuk dead letter. Ia hanya di-backoff
dan di-read ulang, karena penyebabnya biasanya read model yang sedang tidak
tersedia dan akan pulih sendiri.

## Quick start

Butuh PostgreSQL sendiri dan broker Kafka:

```bash
docker compose up -d --build
```

Untuk memakai stack dan broker yang sama dengan repository root:

```bash
docker compose -f ../docker-compose.yml up -d --build
```

Dijalankan terpisah dari host:

```bash
KAFKA_BROKERS=host.docker.internal:9092 docker compose up -d --build
```

## API

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health`, `/healthz` | liveness, tidak bergantung dependency |
| `GET` | `/readyz` | readiness, ping database dengan timeout 3s |
| `GET` | `/version` | versi dan build info |

`/readyz` sengaja **tidak** memeriksa Kafka. Consumer yang tertinggal tetap
melayani data yang benar, hanya lebih lama, dan mengambil instance dari load
balancer tidak akan membuat read model lebih(mutakhir). Endpoint status yang
melaporkan lag.

### Products

Semua route adalah `GET`. Menulis adalah urusan product-service.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/products` | list dengan filter dan pagination |
| `GET` | `/api/v1/products/{id}` | ambil satu produk |
| `GET` | `/api/v1/products/{id}/variants` | list variants |
| `GET` | `/api/v1/listing/status` | progres proyeksi |

### Filter list

| Parameter | Default | Description |
|-----------|---------|-------------|
| `category_id` | - | filter kategori |
| `status` | - | `draft`, `active`, `archived` |
| `min_price_cents` | - | batas bawah harga |
| `max_price_cents` | - | batas atas harga |
| `limit` | `20` | maksimal `100` |
| `offset` | `0` | untuk pagination |
| `include_variants` | `false` | sertakan variants di respons |

```bash
curl 'localhost:8083/api/v1/products?category_id=1&status=active&limit=10'
```

`status` yang tidak dikirim berarti **tanpa filter**, bukan `draft`. Keduanya
harus dibedakan: apply `draft` ke request yang tidak menyinya akan menyembunyikan
setiap produk aktif dan menjawab permintaan seluruh katalog dengan draft saja.

### `q` ditolak, bukan diabaikan

`q` tidak lagi ada di service ini. `GET /api/v1/products?q=aurora` menjawab `400`
yang menunjuk ke
[product-search-service](../product-search-service/):

```json
{"error": "q is not supported here: free-text search moved to product-search-service, use GET /api/v1/search/products?q=..."}
```

Menolak, bukan mengabaikan, adalah pilihan yang disengaja. Kalau `q` diabaikan
diam-diam, client yang meminta pencarian akan menerima seluruh katalog tanpa
filter - dan jawaban itu tidak bisa dibedakan dari jawaban yang sah. `400` yang
menyebut service penggantinya mengubah kesalahan diam-diam jadi error yang bisa
diperbaiki dalam satu langkah.

`q=` (nilai kosong) juga ditolak, karena kalau tidak akan mencocokkan setiap
produk, yaitu jawaban salah yang sama dengan kostum berbeda.

Migrasi `0002_drop_search_index.sql` menghapus `idx_product_listings_name_lower`.
Index itu dulu melayani `q`; sekarang tidak ada query yang memakainya, tapi ia
tetap akan ditulis pada setiap insert dan update produk di tabel yang ditulis
consumer, untuk query yang tidak pernah datang. Memindahkan pencarian ke service
tersendiri berarti biaya index borongan itu dibayar di read model milik search,
yang sanggup menambahkannya lagi tanpa membuat tabel browse ikut menanggung.

### Contoh

```bash
curl -X POST localhost:8082/api/v1/products \
  -H 'Content-Type: application/json' \
  -d '{"category_id":1,"name":"Aurora Phone 15 Pro","price_cents":12999000,"stock":25}'

# produk yang sama, dibaca dari read model
curl localhost:8083/api/v1/products/1
```

### Status

```bash
curl localhost:8083/api/v1/listing/status
```

```json
{"products": 128, "applied_events": 402, "healthy": true}
```

`products: 0` dengan `healthy: true` berarti consumer belum Catch up atau tidak
pernah berjalan, bukan berarti katalognya memang kosong - dan itulah alasan
endpoint ini ada.

### gRPC

`productlisting.v1.ProductListingService` pada port `9093`:

| RPC | Description |
|-----|-------------|
| `GetProduct` | satu produk, opsional dengan variants |
| `ListProducts` | list dengan filter |
| `ListVariants` | variants dari satu produk |

```bash
grpcurl -plaintext localhost:9093 list
grpcurl -plaintext -d '{"category_id":1}' \
  localhost:9093 productlisting.v1.ProductListingService/ListProducts
```

### Status codes

| Code | Kapan |
|------|-------|
| `200` | sukses read |
| `400` | filter tidak valid |
| `404` | produk tidak ada di read model |
| `405` | route mutasi dipanggil; tulis ke product-service |
| `500` | kegagalan internal, detail hanya di log |

`404` berarti "belum ada di read model". Differences dengan "produk tidak ada"
sengaja tidak dibedakan, karena consumer akan menyusul sendiri dan pemanggil tidak
bisa melakukan apa pun dengan perbedaan itu.

## Database

`product_listings` dan `product_listing_variants` adalah read model-nya.
Primary key-nya adalah id dari product-service, jadi proyeksi tidak pernah perlu
memetakan satu ruang id ke ruang lain, dan aggregate id sebuah event langsung
dipakai sebagai key baris.

```sql
CREATE TABLE IF NOT EXISTS product_listings (
    product_id      BIGINT       PRIMARY KEY,
    category_id     BIGINT       NOT NULL,
    name            VARCHAR(255) NOT NULL,
    slug            VARCHAR(255) NOT NULL,
    description     TEXT,
    brand           VARCHAR(255),
    price_cents     BIGINT       NOT NULL,
    currency        CHAR(3)      NOT NULL,
    stock           INTEGER      NOT NULL DEFAULT 0,
    status          VARCHAR(16)  NOT NULL,
    primary_media_id BIGINT,
    created_at      TIMESTAMPTZ  NOT NULL,
    updated_at      TIMESTAMPTZ  NOT NULL,

    projected_at    TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_id   UUID         NOT NULL,

    CONSTRAINT product_listings_slug_unique UNIQUE (slug),
    CONSTRAINT product_listings_status_valid CHECK (status IN ('draft', 'active', 'archived')),
    CONSTRAINT product_listings_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT product_listings_stock_non_negative CHECK (stock >= 0)
);

CREATE INDEX IF NOT EXISTS idx_product_listings_active
    ON product_listings(created_at DESC)
    WHERE status = 'active';
```

Index di bawah mencerminkan filter yang diekspos API read.-Inilah justifikasi
seluruh pemisahan.read side: index yang harus.write path maintenance di setiap
insert dan update demi query yang tidak pernah dilayaninya.

Variant diproyeksikan ke tabel terpisah, bukan ditanam sebagai array JSONB di baris
produk. Baris dengan array akan ditulis ulang setiap perubahan variant, dan kasus
umumnya adalah halaman produk tanpa variant sama sekali.

`projected_at` sengaja dipisahkan dari `updated_at`: produk bisa dipublikasikan
ulang tanpa berubah, dan operator yang menelusuri lag perlu bisa membedakan
keduanya.

### processed_events

```sql
CREATE TABLE IF NOT EXISTS processed_events (
    event_id      UUID        PRIMARY KEY,
    event_type    TEXT        NOT NULL,
    aggregate_id  BIGINT      NOT NULL,
    topic         TEXT        NOT NULL,
    partition     INTEGER     NOT NULL,
    kafka_offset  BIGINT      NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Ini adalah gate idempotensi. `event_id` adalah UUID yang unik per event, jadi
redelivery terdeteksi secara pasti, dan pengecekan plus insert adalah satu
pernyataan. Baris di-prune oleh `PROCESSED_EVENT_RETENTION` (default 24 jam),
karena redelivery datang dalam hitungan detik dan broker tidak akan mengulang
offset itu lagi setelah jendela tersebut lewat.

`consumer_checkpoints` menyimpan offset yang sudah diterapkan per partisi. Itu
bookkeeping untuk operator, bukan sumber kebenaran: consumer group yang memegang
offset otoritatif di Kafka. Tabel ini menjawab "sejauh apa read model tertinggal",
yang tidak bisa dijawab broker.

## Configuration

Semua variabel opsional; default pengembangan tersedia agar service bisa start
tanpa konfigurasi.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `listing_user` | database user |
| `DB_PASSWORD` | `listing_pass` | database password |
| `DB_NAME` | `listing_db` | database name |
| `DB_SSLMODE` | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `DB_MAX_OPEN_CONNS` | `25` | batas connection pool |
| `DB_MAX_IDLE_CONNS` | `10` | idle pool, tidak boleh melebihi max open |
| `DB_CONN_MAX_LIFETIME` | `5m` | daur ulang koneksi |
| `DB_CONNECT_TIMEOUT` | `10s` | timeout koneksi awal |
| `KAFKA_BROKERS` | `localhost:9092` | seed broker list, host:port dipisah koma |
| `KAFKA_CLIENT_ID` | `product-listing-service` | identitas consumer di log broker |
| `KAFKA_CONSUMER_GROUP` | `product-listing-service` | semua replika bergabung ke group yang sama |
| `KAFKA_TOPIC` | `product.events.v1` | topik event yang dikonsumsi |
| `KAFKA_DEAD_LETTER_TOPIC` | `product.events.v1.dlq` | topik untuk record yang tidak bisa diterapkan |
| `KAFKA_MAX_POLL_RECORDS` | `200` | batas satu fetch |
| `KAFKA_POLL_TIMEOUT` | `1s` | batas tunggu satu poll |
| `KAFKA_RETRY_BACKOFF` | `1s` | jeda setelah kegagalan transient |
| `CONSUMER_ENABLED` | `true` | matikan consumer untuk menjalankan read API sendiri |
| `PROCESSED_EVENT_RETENTION` | `24h` | berapa lama id event disimpan |
| `PRUNE_INTERVAL` | `1h` | seberapa sering prune berjalan |
| `APP_PORT` | `8083` | listen port REST |
| `GRPC_PORT` | `9093` | listen port gRPC |
| `GRPC_MAX_MESSAGE_SIZE` | `16777216` | batas satu pesan gRPC |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | proteksi slowloris |
| `HTTP_READ_TIMEOUT` | `15s` | batas baca request |
| `HTTP_WRITE_TIMEOUT` | `15s` | batas tulis response |
| `HTTP_IDLE_TIMEOUT` | `60s` | batas keep-alive |
| `SHUTDOWN_TIMEOUT` | `30s` | drain window saat graceful shutdown |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` atau `text` |

`KAFKA_DEAD_LETTER_TOPIC` wajib diisi saat consumer aktif, dan harus berbeda dari
`KAFKA_TOPIC`. Tanpa itu satu record yang rusak akan menahan partisi selamanya;
menaruhnya di topik yang sama berarti consumer akan membaca ulang record yang
justru ditolaknya.

Kredensial database tidak pernah masuk log: yang dicatat adalah `RedactedDSN()`.

> **Catatan keamanan.** Koneksi Kafka memakai transport plaintext, dengan asumsi
> trafik hanya berjalan di jaringan cluster. Untuk deployment lintas jaringan,
> tambahkan konfigurasi TLS dan SASL pada
> `kgo.NewClient(...)` di `internal/messaging/consumer.go`.

## Development

```bash
go run ./cmd/server
go test ./...
go test -race ./...
go test -cover ./...
```

Test tidak butuh database atau Kafka: `projection.Writer` dan
`service.ListingRepository` adalah interface, jadi keduanya diuji dengan stub.
`internal/event` diuji terhadap envelope dalam bentuknya yang sebenarnya, sehingga
perubahan kontrak gagal di test dan bukan diam-diam di consumer.

### Test yang butuh PostgreSQL

Read model adalah proyeksi, jadi kebenarannya ada di SQL-nya, dan propertinya
tidak terlihat dari kode. `internal/repository/projection_sql_test.go`
memverifikasi terhadap PostgreSQL sungguhan:

- Upsert produk **idempoten**: menerapkan event yang sama beberapa kali
  menghasilkan baris yang sama persis, bukan baris tambahan. Inilah dasar
 	at-least-once delivery yang aman di sini.
- Gate deduplikasi **satu statement**, jadi pengecekan dan insert tidak bisa
  terpisah.
- `SetProductStock` hanya menulis kolom stok. Event stok tidak membawa nilai kolom
  lain, jadi menulis seluruh baris akan menimpa update produk yang lebih baru yang
  sudah diproyeksikan.
- Partial index `idx_product_listings_active` **benar-benar dipakai** oleh query
  browse saat tabel sudah berisi ribuan draft.
- Setiap filter yang mencapai read model terikat sebagai parameter, jadi bentuk SQL
  tidak pernah berubah mengikuti input.

Test yang dulu menguji escaping wildcard dan case-insensitivity punya putaran kedua
kini di
[product-search-service](../product-search-service/#test-yang-butuh-postgresql),
karena filter pencarian tidak lagi melewati service ini.

Test skip sendiri kalau `POSTGRES_TEST_DSN` tidak diisi, jadi `go test ./...`
tetap jalan tanpa database:

```bash
docker run -d --name pgtest -p 55432:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine
docker exec pgtest psql -U postgres -q -c "CREATE DATABASE listing_db"
docker exec -i pgtest psql -U postgres -q -d listing_db < internal/database/migrations/0001_init.sql

POSTGRES_TEST_DSN="postgres://postgres:pw@127.0.0.1:55432/listing_db?sslmode=disable" \
  go test ./internal/repository/
```

Test mem-`TRUNCATE` tabel read model, jadi arahkan ke database khusus, bukan ke
database yang sedang dipakai stack.->

Untuk menjalankan read API tanpa consumer, misalnya saat membangun read model
secara manual:

```bash
CONSUMER_ENABLED=false go run ./cmd/server
```

### Regenerasi stub gRPC

Stub di `gen/` di-commit. Setelah mengubah `.proto`:

```bash
./hack/gen-proto.sh
```

## License

MIT
