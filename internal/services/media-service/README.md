# Media Service

REST + gRPC service untuk menyimpan metadata media dan object storage. Gambar
dan video tidak pernah melewati service ini: metadata disimpan di PostgreSQL,
sementara byte-nya berada di MinIO dan diakses client langsung lewat
**presigned URL**.

## Fitur

- **Presigned upload** - client `PUT` langsung ke MinIO, service tidak jadi proxy
- **Presigned download** - URL berlaku terbatas, dibuat on demand
- **Upload lifecycle** - `pending` lalu `ready` setelah object diverifikasi
- **Ukuran dan ETag dari store** - nilai yang dilaporkan client tidak dipercaya
- **Object key unik** - `media/<yyyy>/<mm>/<timestamp>-<random>`, tidak bisa ditimpa
- **Graceful shutdown** - `SIGTERM`/`SIGINT` untuk REST dan gRPC
- **Readiness menyeluruh** - cek PostgreSQL **dan** MinIO
- **gRPC + reflection** - `grpcurl` jalan tanpa file `.proto` lokal

## Arsitektur

```
media-service/
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
│   │       └── 0001_init.sql           # schema, index, trigger
│   ├── grpcsrv/
│   │   └── media_server.go             # adapter gRPC -> service
│   ├── handler/
│   │   ├── media_handler.go            # HTTP handlers
│   │   ├── health_handler.go           # liveness, readiness, version
│   │   └── middleware.go               # logging, recovery, headers
│   ├── httpx/
│   │   └── httpx.go                    # JSON helpers, error mapping
│   ├── model/
│   │   └── media.go                    # domain types
│   ├── repository/
│   │   └── media_repository.go         # SQL
│   ├── service/
│   │   └── media_service.go            # business rules
│   ├── storage/
│   │   ├── storage.go                  # Store interface
│   │   └── minio.go                    # implementasi S3/MinIO
│   └── version/
│       └── version.go                  # build metadata
├── docker/
│   └── minio/
│       └── Dockerfile                  # build MinIO dari source
├── gen/mediapb/                        # generated, di-commit
├── hack/
│   └── gen-proto.sh                    # regenerasi stub
├── Dockerfile
└── docker-compose.yml
```

Aliran dependency satu arah: `handler -> service -> repository -> database`, dan
`grpcsrv -> service -> storage`. `service` mendefinisikan interface untuk
`repository` dan `storage`, sehingga kedua lapisan bisa diuji dengan stub tanpa
database dan tanpa MinIO.

## MinIO dibangun dari source

`docker pull minio/minio` gagal dengan `repository does not exist`. MinIO
Community Edition berhenti menerbitkan prebuilt image pada Oktober 2025, lalu
repository `minio/minio` dan `minio/mc` dihapus dari Docker Hub pada September
2026. Endpoint `dl.min.io` juga sekarang mengembalikan `410 Gone`.

Karena itu `docker/minio/Dockerfile` membangun tag rilis upstream terakhir dari
source:

```dockerfile
RUN CGO_ENABLED=0 go install github.com/minio/minio@RELEASE.2025-10-15T17-29-55Z
```

Hasilnya image lokal biasa, jadi stack compose tidak berubah sama sekali.

> **Catatan penting.** MinIO Community Edition sekarang archived dan tidak lagi
> menerima patch keamanan. Rilis `2025-10-15` adalah yang terakhir terbit dan
> sudah berisi perbaikan CVE-2025-62506, tetapi rilis berikutnya tidak pernah
> ada. Untuk produksi, pakai object storage S3-compatible yangmanaged - AWS S3,
> Cloudflare R2, atau Garage - lalu ganti `MINIO_ENDPOINT` dan kredensialnya.
> Kode tidak perlu berubah sama sekali karena semuanya lewat `storage.Store`.

## Quick start

```bash
# dari root repository, jalankan seluruh stack
docker compose -f ../docker-compose.yml up -d --build

# atau hanya media-service
docker compose up -d --build
```

Pantau log sampai migrasi selesai:

```bash
docker compose logs -f app
curl -fsS localhost:8081/readyz    # {"status":"ready"}
```

## Alur upload

```
Client                 media-service              MinIO
  │                          │                        │
  │ POST /api/v1/media       │                        │
  │─────────────────────────>│ insert row (pending)   │
  │                          │ presign PUT            │
  │<─────────────────────────│ { media, upload.url }   │
  │                          │                        │
  │ PUT <upload.url>         │                        │
  │───────────────────────────────────────────────────>│
  │                          │                        │
  │ POST /media/{id}/complete│                        │
  │─────────────────────────>│ StatObject             │
  │                          │───────────────────────>│
  │                          │ update row (ready)     │
  │<─────────────────────────│ { media }               │
  │                          │                        │
  │ GET /media/{id}/download-url                      │
  │─────────────────────────>│ presign GET            │
  │<─────────────────────────│ { url, expires_at }    │
  │                          │                        │
  │ GET <download url>                                │
  │───────────────────────────────────────────────────>│
```

`size_bytes` dan `etag` pada respons `/complete` berasal dari MinIO, bukan dari
klaim client, karena client tidak bisa dipercaya untuk mendeskripsikan
unggahan-nya sendiri.

## API

### Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health`, `/healthz` | liveness, tidak bergantung dependency |
| `GET` | `/readyz` | readiness, ping database **dan** MinIO dengan timeout 3s |
| `GET` | `/version` | versi dan build info |

### Media

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/v1/media` | daftarkan media, balik upload URL |
| `GET` | `/api/v1/media` | list media dengan filter dan pagination |
| `GET` | `/api/v1/media/{id}` | ambil satu record |
| `DELETE` | `/api/v1/media/{id}` | hapus object lalu metadata, `204` |
| `GET` | `/api/v1/media/{id}/upload-url` | terbitkan ulang upload URL |
| `POST` | `/api/v1/media/{id}/complete` | verifikasi upload, tandai `ready` |
| `GET` | `/api/v1/media/{id}/download-url` | presigned download URL |

### Contoh

```bash
# 1. daftar media
curl -X POST localhost:8081/api/v1/media \
  -H 'Content-Type: application/json' \
  -d '{"owner_id":"user-1","filename":"red.png","content_type":"image/png"}'
```

```json
{
  "media": {
    "id": 1,
    "owner_id": "user-1",
    "filename": "red.png",
    "content_type": "image/png",
    "size_bytes": 0,
    "etag": null,
    "bucket": "media",
    "object_key": "media/2026/09/1790782014512076549-27b9780e4398ec90.png",
    "kind": "image",
    "status": "pending",
    "created_at": "2026-09-30T15:26:54.513824Z",
    "updated_at": "2026-09-30T15:26:54.513824Z"
  },
  "upload": {
    "url": "http://minio:9000/media/media/2026/09/...-27b9780e4398ec90.png?X-Amz-...",
    "method": "PUT",
    "headers": { "Content-Type": "image/png" },
    "expires_at": "2026-09-30T15:41:54.517529Z"
  }
}
```

`kind` bersifat opsional: bila dikosongkan, service menyimpulkan dari
`content_type`, dan dari extension file sebagai cadangan.

```bash
# 2. upload langsung ke MinIO, service tidak terlibat sama sekali
curl -X PUT "$UPLOAD_URL" -H 'Content-Type: image/png' --data-binary @red.png

# 3. konfirmasi
curl -X POST localhost:8081/api/v1/media/1/complete

# 4. ambil URL download
curl localhost:8081/api/v1/media/1/download-url
```

### Query parameters

| Parameter | Endpoint | Default | Description |
|-----------|----------|---------|-------------|
| `owner_id` | `GET /media` | - | filter pemilik |
| `status` | `GET /media` | - | `pending` atau `ready` |
| `limit` | `GET /media` | `20` | maksimal `100` |
| `offset` | `GET /media` | `0` | untuk pagination |
| `presign_ttl_seconds` | `GET /media/{id}/download-url` | dari config | maksimal 7 hari |

### gRPC

`media.v1.MediaService` pada port `9091`:

| RPC | Description |
|-----|-------------|
| `RegisterMedia` | create record `pending`, balik upload URL |
| `GetMedia` | metadata saja, tidak menyentuh MinIO |
| `ListMedia` | paging per owner, terbaru dulu |
| `GetUploadURL` | upload URL baru untuk upload yang belum selesai |
| `CompleteUpload` | verifikasi object, tandai `ready` |
| `GetDownloadURL` | presigned GET URL |
| `BatchGetDownloadURLs` | resolusi banyak id dalam satu panggilan |
| `DeleteMedia` | hapus object dan metadata |

```bash
grpcurl -plaintext localhost:9091 list
grpcurl -plaintext -d '{"ids":[1]}' localhost:9091 media.v1.MediaService/BatchGetDownloadURLs
```

### Status codes

| Code | Kapan |
|------|-------|
| `200` | sukses read / complete |
| `201` | register, dengan header `Location` |
| `204` | delete, tanpa body |
| `400` | id tidak valid, JSON rusak, field tak dikenal, content type tidak valid |
| `404` | media tidak ada |
| `409` | upload URL untuk media yang sudah `ready`, atau object key bentrok |
| `500` | kegagalan internal, detail hanya di log |

## Database

```sql
CREATE TABLE IF NOT EXISTS media (
    id           BIGSERIAL PRIMARY KEY,
    owner_id     VARCHAR(255) NOT NULL,
    filename     VARCHAR(255) NOT NULL,
    content_type VARCHAR(127) NOT NULL,
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    etag         VARCHAR(255),
    bucket       VARCHAR(63)  NOT NULL,
    object_key   VARCHAR(512) NOT NULL,
    kind         VARCHAR(16)  NOT NULL DEFAULT 'other',
    status       VARCHAR(16)  NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT media_bucket_object_key_unique UNIQUE (bucket, object_key),
    CONSTRAINT media_size_non_negative CHECK (size_bytes >= 0),
    CONSTRAINT media_status_valid CHECK (status IN ('pending', 'ready')),
    CONSTRAINT media_kind_valid CHECK (kind IN ('image', 'video', 'audio', 'document', 'other'))
);
```

Tabel ini menyimpan metadata saja. Constraint `UNIQUE (bucket, object_key)`
mencegah dua record mengklaim object yang sama, dan CHECK menjaga agar nilai
yang disimpan tetap sesuai dengan bentuk yang sudah dinormalisasi di service.

Migrasi di-embed ke binary dan dicatat di tabel `schema_migrations`. Menjalankan
ulang saat boot bersifat idempotent, termasuk ketika beberapa replica start
bersamaan.

`updated_at` dijaga trigger `media_set_updated_at`, bukan oleh aplikasi.

## Configuration

Semua variabel opsional; default pengembangan tersedia agar service bisa start
tanpa konfigurasi. Nilai invalid ditolak saat start, bukan saat dipakai.

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `media_user` | database user |
| `DB_PASSWORD` | `media_pass` | database password |
| `DB_NAME` | `media_db` | database name |
| `DB_SSLMODE` | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `DB_MAX_OPEN_CONNS` | `25` | batas connection pool |
| `DB_MAX_IDLE_CONNS` | `10` | idle pool, tidak boleh melebihi max open |
| `DB_CONN_MAX_LIFETIME` | `5m` | daur ulang koneksi |
| `DB_CONNECT_TIMEOUT` | `10s` | timeout koneksi awal |
| `MINIO_ENDPOINT` | `localhost:9000` | host:port, tanpa scheme |
| `MINIO_ACCESS_KEY` | `minioadmin` | access key |
| `MINIO_SECRET_KEY` | `minioadmin` | secret key |
| `MINIO_BUCKET` | `media` | bucket tujuan, divalidasi saat start |
| `MINIO_REGION` | `us-east-1` | region untuk bucket lookup |
| `MINIO_USE_SSL` | `false` | `true` untuk https |
| `MINIO_PRESIGN_UPLOAD_TTL` | `15m` | masa berlaku upload URL |
| `MINIO_PRESIGN_DOWNLOAD_TTL` | `1h` | masa berlaku download URL |
| `MINIO_ENSURE_BUCKET` | `true` | buat bucket saat boot jika belum ada |
| `APP_PORT` | `8081` | listen port REST |
| `GRPC_PORT` | `9091` | listen port gRPC |
| `GRPC_MAX_MESSAGE_SIZE` | `16777216` | batas satu pesan gRPC |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | proteksi slowloris |
| `HTTP_READ_TIMEOUT` | `15s` | batas baca request |
| `HTTP_WRITE_TIMEOUT` | `15s` | batas tulis response |
| `HTTP_IDLE_TIMEOUT` | `60s` | batas keep-alive |
| `SHUTDOWN_TIMEOUT` | `30s` | drain window saat graceful shutdown |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` atau `text` |

Password database dan secret key MinIO tidak pernah masuk log: yang dicatat
adalah `RedactedDSN()` dan `RedactedMinIO()`.

## Development

```bash
go run ./cmd/server
go test ./...
go test -race ./...
go test -cover ./...
```

Butuh PostgreSQL 17 dan MinIO yang bisa dijangkau. Tanpa keduanya service tetap
mulai, lalu `/readyz` menjawab `503` sementara liveness tetap `200`, supaya
container tidak masuk restart loop karena dependency yang belum siap.

### Regenerasi stub gRPC

Stub di `gen/` di-commit agar build hanya butuh Go. Setelah mengubah `.proto`:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
./hack/gen-proto.sh
```

## License

MIT
