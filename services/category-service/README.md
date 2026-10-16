# Category Service

REST API untuk mengelola kategori global hierarki (nested) serta kontrak gRPC
untuk kategori lokal per merchant, menggunakan Go 1.27 dan PostgreSQL 17.
Dibangun dan dikemas dengan Nix flake, dengan image container yang reproducible.

## Fitur

- **Nested categories** - parent-child tanpa batas kedalaman
- **Tree structure** - ambil kategori sebagai tree nested (`/tree`)
- **Ancestor path** - daftar ancestor dari parent terdekat sampai root
- **Circular reference detection** - update yang membuat siklus ditolak `409`
- **Cascade delete** - menghapus kategori ikut menghapus seluruh subtree
- **Kategori global dan lokal** - kategori admin dibagi bersama; kategori merchant terisolasi per tenant
- **Graceful shutdown** - `SIGTERM`/`SIGINT` dengan drain window
- **Health & readiness probes** - `/health`, `/healthz`, `/readyz`
- **Version endpoint** - `/version` untuk konfirmasi build

## Arsitektur

```
sample/
├── cmd/
│   └── server/
│       └── main.go                    # entrypoint, wiring, lifecycle
├── internal/
│   ├── apperror/
│   │   └── apperror.go                # sentinel error domain
│   ├── config/
│   │   └── config.go                  # env config + validasi
│   ├── database/
│   │   ├── database.go                # connection pool, migrator
│   │   └── migrations/
│   │       └── 0001_init.sql          # schema, index, trigger
│   ├── handler/
│   │   ├── category_handler.go        # HTTP handlers
│   │   ├── health_handler.go          # liveness, readiness, version
│   │   └── middleware.go              # logging, recovery, headers
│   ├── httpx/
│   │   └── httpx.go                   # JSON helpers, error mapping
│   ├── model/
│   │   └── category.go                # domain types
│   ├── repository/
│   │   └── category_repository.go     # SQL
│   ├── service/
│   │   └── category_service.go        # business rules
│   └── version/
│       └── version.go                 # build metadata
├── flake.nix                          # build, image, devshell, checks
├── go.mod
└── go.sum
```

Aliran dependency satu arah: `handler -> service -> repository -> database`.
`handler` mendefinisikan interface untuk `service` yang dibutuhkannya, dan
`service` untuk `repository`, sehingga kedua lapisan bisa diuji dengan stub
tanpa database.

## Prerequisites

- Nix dengan flakes (`nix --extra-experimental-features 'nix-command flakes'`)
- PostgreSQL 17 (disediakan di devshell, atau via Docker)
- Docker, opsional, hanya untuk memuat image

## Quick start dengan Nix

```bash
# Build binary
nix build

# Jalankan
./result/bin/category-service

# Build image container
nix build .#image

# Muat image ke Docker daemon
nix run .#load-image

# Jalankan image + postgres via compose
nix run .#load-image
docker compose -f "$(nix eval --raw .#compose)" up -d

# Push ke registry tanpa docker daemon di loop
nix run .#push-image ghcr.io/your-org/category-service

# Build multi-arch (butuh binfmt / emulation)
nix build .#image --system aarch64-linux
```

## Development

```bash
# Masuk devshell (Go, gopls, golangci-lint, PostgreSQL 17, docker client)
nix develop

# ATAU pakai direnv, otomatis saat masuk direktori
direnv allow

go run ./cmd/server
go test ./...
go test -race ./...
go test -cover ./...
```

`nix develop` mengekspor `DB_*` dan `APP_PORT` sehingga `go run` langsung
terhubung ke PostgreSQL lokal.

## Docker Compose

Untuk workflow tanpa Nix, `Dockerfile` dan `docker-compose.yml` tetap tersedia:

```bash
docker-compose up --build
```

Keduanya menghasilkan binary yang sama; compose sekarang memakai image dari
Nix, sedangkan `Dockerfile` dipertahankan untuk mesin tanpa Nix.

## API

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health`, `/healthz` | liveness, tidak bergantung database |
| `GET` | `/readyz` | readiness, ping database dengan timeout 3s |
| `GET` | `/version` | versi dan build info |

### Categories

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/categories` | buat kategori baru |
| `GET` | `/api/v1/categories` | list flat, diurutkan `name` |
| `GET` | `/api/v1/categories/tree` | list sebagai tree nested |
| `GET` | `/api/v1/categories/{id}` | ambil satu kategori |
| `PUT` | `/api/v1/categories/{id}` | update sebagian field |
| `DELETE` | `/api/v1/categories/{id}` | hapus beserta subtree, `204` |
| `GET` | `/api/v1/categories/{id}/children` | anak langsung |
| `GET` | `/api/v1/categories/{id}/ancestors` | ancestor, parent lebih dulu |
| `GET` | `/api/v1/categories/{id}/impact` | jumlah descendant yang akan terhapus |

### Contoh

```bash
# Buat root, child, grandchild
curl -X POST localhost:8080/api/v1/categories \
  -H 'Content-Type: application/json' \
  -d '{"name":"Electronics","description":"Gadgets"}'

curl -X POST localhost:8080/api/v1/categories \
  -H 'Content-Type: application/json' \
  -d '{"name":"Smartphones","parent_id":1}'

curl -X POST localhost:8080/api/v1/categories \
  -H 'Content-Type: application/json' \
  -d '{"name":"iPhone","parent_id":2}'
```

`parent_id` dan `description` yang kosong diserialisasi sebagai `null`, bukan
bentuk internal `sql.NullInt64`.

```json
{
  "id": 1,
  "name": "Electronics",
  "description": "Gadgets",
  "parent_id": null,
  "created_at": "2026-09-30T13:20:40.664912Z",
  "updated_at": "2026-09-30T13:20:40.664912Z"
}
```

Tree:

```json
[
  {
    "id": 1,
    "name": "Electronics",
    "parent_id": null,
    "children": [
      { "id": 2, "name": "Smartphones", "parent_id": 1, "children": [] }
    ]
  }
]
```

### Partial update

`PUT` hanya mengubah field yang dikirim. `null` berarti kosongkan field, bukan
"jangan sentuh":

```bash
# hanya ganti nama, description tetap
curl -X PUT localhost:8080/api/v1/categories/1 \
  -H 'Content-Type: application/json' \
  -d '{"name":"Electronics & Gadgets"}'

# kosongkan description
curl -X PUT localhost:8080/api/v1/categories/1 \
  -H 'Content-Type: application/json' \
  -d '{"description":null}'

# lepas dari parent, jadi root
curl -X PUT localhost:8080/api/v1/categories/3 \
  -H 'Content-Type: application/json' \
  -d '{"parent_id":null}'
```

### Status codes

| Code | Kapan |
|------|-------|
| `200` | sukses read / update |
| `201` | create, dengan header `Location` |
| `204` | delete, tanpa body |
| `400` | id tidak valid, JSON rusak, field tak dikenal, parent tidak ada |
| `404` | kategori tidak ada |
| `405` | method tidak diizinkan pada route |
| `409` | update akan membuat circular reference |
| `500` | kegagalan internal, detail hanya di log |

## Database

```sql
CREATE TABLE categories (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    description TEXT,
    parent_id   INTEGER REFERENCES categories(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT categories_not_own_parent CHECK (parent_id IS NULL OR parent_id <> id)
);
```

Migrasi ada di `internal/database/migrations/`, di-embed ke binary, dan
dicatat di tabel `schema_migrations`. Menjalankan ulang saat boot bersifat
idempotent, termasuk ketika beberapa replica start bersamaan.

`updated_at` dijaga trigger `categories_set_updated_at`, dan constraint
`categories_not_own_parent` menolak self-parent di level database, bukan hanya
di aplikasi.

## Configuration

Semua variabel opsional; default développement tersedia agar service bisa start
tanpa konfigurasi. Nilai invalid ditolak saat start, bukan saat dipakai.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `category_user` | database user |
| `DB_PASSWORD` | `category_pass` | database password |
| `DB_NAME` | `category_db` | database name |
| `DB_SSLMODE` | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `DB_MAX_OPEN_CONNS` | `25` | batas connection pool |
| `DB_MAX_IDLE_CONNS` | `10` | idle pool, tidak boleh melebihi max open |
| `DB_CONN_MAX_LIFETIME` | `5m` | daur ulang koneksi |
| `DB_CONNECT_TIMEOUT` | `10s` | timeout koneksi awal |
| `APP_PORT` | `8080` | listen port |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | proteksi slowloris |
| `HTTP_READ_TIMEOUT` | `15s` | batas baca request |
| `HTTP_WRITE_TIMEOUT` | `15s` | batas tulis response |
| `HTTP_IDLE_TIMEOUT` | `60s` | batas keep-alive |
| `SHUTDOWN_TIMEOUT` | `30s` | drain window saat graceful shutdown |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` atau `text` |

Password tidak pernah masuk log: yang dicatat adalah `RedactedDSN()`.

## Image

Dibangun dengan `dockerTools.buildImage`, jadi isinya hanya static binary plus
CA certificates. Tidak ada shell, package manager, atau `nix` CLI di dalam
image, dan container berjalan sebagai user non-root `65532`.

```bash
# image sebagai tarball
nix build .#image
# stream langsung ke docker load
nix run .#load-image
```

Ukuran image sekitar 3.8 MB terkompresi. Binary di-build dengan
`CGO_ENABLED=0` dan `-s -w`, jadi sepenuhnya statis.

## Checks

```bash
nix flake check     # build, image, gofmt, go vet
go test ./...       # unit test
go test -race ./... # race detector
nix fmt             # format flake.nix
```

`nix flake check` gagal kalau ada file yang belum `gofmt`-clean, jadi format
terjaga di CI tanpa langkah terpisah.

## License

MIT
