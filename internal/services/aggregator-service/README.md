# Aggregator Service

GraphQL composition layer in front of the five platform services. It owns no data
and no database.

```
    POST /query ──────> ┌──────────────────────────────┐
                         │     aggregator-service      │
                         │  gqlgen, one GraphQL API    │
                         └──────┬───┬───┬───┬───┬──────┘
                                │   │   │   │   │
        gRPC        gRPC  gRPC   │   │   │   └────── gRPC  product-search-service  :9094
          │           │     │    │   │   └────────── gRPC  product-listing-service :9093
          │           │     │    │   └────────────── gRPC  product-service        :9092
          │           │     │    └────────────────── gRPC  media-service          :9091
          │           │     └─────────────────────── gRPC  category-service       :9090
          │           │
          │           └── gRPC, writes only
          └────────────── gRPC, reads only

    REST, and only these two endpoints
          └── HTTP ──> product-listing-service /api/v1/listing/status
          └── HTTP ──> product-search-service  /api/v1/search/status
```

## Kenapa ada

Lima service di repo ini sudah menyelesaikan masalahnya masing-masing: kategori punya
hierarki, media punya presigned URL, produk punya sisi tulis dan dua sisi baca yang
terpisah oleh event stream. Yang belum ada adalah satu tempat di mana client bisa
mengajukan satu pertanyaan dan menerima satu jawaban.

Komposisi REST harus direkayasa. Begitu client butuh kategori **dan** gambar **dan**
breadcrumb untuk satu halaman produk, ia melakukan tiga panggilan ke tiga service lalu
menulis kode penggabungan sendiri di setiap client. GraphQL dengan batch resolver
menghapus penggabungan itu dari client: satu query, satu round trip, dan setiap service
dipanggil satu kali per halaman.

Ada dua masalah yang diselesaikan aggregator ini secara khusus:

**N+1 dihilangkan, bukan disembunyikan.** `products { id category { name }
primaryMedia { downloadUrl } }` untuk 20 produk menghasilkan tiga panggilan gRPC, bukan
42. Ini bukan caching atau rate limiting; `gqlgen` mengelompokkan resolution per list,
sehingga jumlah panggilan adalah property dari bentuk query itu sendiri, bukan dari Lihat
[Batch resolver](#batch-resolver).

Satu pengecualian perlu dijelaskan, karena bentuk kode menyiratkan sebaliknya.
`Media.downloadUrl` **bukan** field resolver; URL-nya ditempelkan ke record `Media` oleh
batch yang sama yang mengambil metadata-nya. Alasannya struktural: `gqlgen` hanya
menggabungkan batch resolver saat ia marshal sebuah list, sedangkan `primaryMedia` adalah
satu objek per produk. Field resolver di `Media.downloadUrl` akan berjalan sekali per
produk — 20 panggilan untuk satu halaman. Menjadikan URL ini property dari record,
bukan field resolver, membuat biayanya satu panggilan per halaman. Lihat
[Batch resolver: field yang tidak bisa di-batch](#batch-resolver-field-yang-tidak-bisa-di-batch).

**Kegagalan sebagian tidak menghapus halaman.** `category-service` mati →
`Product.category` bernilai null dan errors array menjelaskan kenapa. Halaman produk
tetap lengkap. Ini alasannya cross-service edge di schema nullable, dan ini
alasan utama meletakkan composition layer di sini. Tanpa itu, satu
dependency yang lambat menghapus seluruh response.

## Konsistensi: baca dari proyeksi, tulis dari sisi tulis

Ini yang paling sering disalahpahami, jadi ada di schema secara eksplisit.

`product-service` tidak punya RPC baca. `GET /api/v1/products` di sana menjawab 405.
Membaca produk dilakukan lewat `product-listing-service` dan
`product-search-service`, yang keduanya read model dibangun dari event stream. Jadi
produk yang dibuat sebentar lalu mungkin belum terbaca: event-nya masih di Kafka,
dan proyeksinya belum menyusul.

Konsekuensinya ada di schema:

| Field | Arti |
|-------|------|
| `WriteProductResult.projected` | false segera setelah create. Write sudah commit, produk sudah nyata, hanya belum terlihat |
| `Product.projectedAt` | kapan read model menulis baris itu. null = belum diproyeksi |
| `Query.projectionHealth` | seberapa jauh kedua read model tertinggal |

Tanpa `projected`, client tidak bisa membedakan "commit dan terlihat" dari "commit dan
belum terlihat", dan cara paling umum client untuk mengetahuinya adalah mencoba ulang —
yang menghasilkan duplikat. Itu sebabnya field itu ada.

Nilainya `false` dan tetap `false` dengan kontrak saat ini, bukan hanya sesaat setelah
create. Proyeksi listing melaporkan berapa event yang sudah dikonsumsi, bukan posisi
write ini di dalam stream, dan `product-service` tidak mengembalikan posisi yang
dihasilkan write-nya — jadi "apakah baris saya sudah terlihat" memang tidak bisa
dijawab. `true` di sini akan jadi tebakan, dan client yang percaya tebakan itu lalu
tidak menemukan apa-apa pada read berikutnya akan menyimpulkan write-nya hilang lalu
mencobanya lagi. Field-nya tetap ada karena bentuknya benar dan menghapusnya akan
membreak client; mengisinya butuh nomor sekuens dari salah satu sisi. `appliedEvents`
dikembalikan berdampingan supaya client bisa memantau proyeksinya sendiri.

Tidak ada kompensasi untuk itu, dan tidak seharusnya: eventual consistency adalah
property dari arsitektur event-driven ini, bukan cacat yang perlu disamarkan.
Aggregator membuatnya terlihat, bukan menghapusnya.

## Batch resolver

Ini keputusan yang paling menentukan di seluruh service.

`graph/gqlgen.yml` mengaktifkan `resolver.batch: true`. Setiap field yang butuh
resolver di sebuah object type jadi batch resolver, yang menerima **semua** parent
dari satu list sekaligus:

```go
func (r *productResolver) Category(ctx context.Context, objs []*model.Product) ([]*model.Category, error)
```

`gqlgen` memanggilnya sekali untuk satu list marshal, lalu menyebarkan hasilnya ke
masing-masing objek. Jadi untuk halaman 20 produk:

| Query | gRPC calls | Tanpa batching |
|-------|-----------|----------------|
| `products { id category { name } }` | 2 | 21 |
| `products { id primaryMedia { downloadUrl } }` | 3 | 21 |
| keduanya | 4 | 42 |

Tiga panggilan untuk `primaryMedia { downloadUrl }` itu: satu untuk metadata media,
satu untuk presigned URL, satu untuk produknya sendiri. Yang penting adalah jumlahnya
tidak berubah ketika halamannya tumbuh.

Ada satu bentuk yang perlu disebut: `categories { ancestors {…} children {…}
productCount }` men-select tiga field yang semuanya butuh seluruh hierarki, jadi tanpa
memo per request itu tiga fetch untuk bytes yang identik. Cache-nya di `RequestCache`
(`graph/resolvers_common.go`), dipasang sebagai middleware oleh `main`, dengan
single-flight supaya dua field yang di-marshal bersamaan tidak sama-sama miss.

Yang membuatnya deterministik: pengelompokan terikat secara struktural pada satu list
marshal, bukan pada time window. DataLoader menggabungkan dalam jendela waktu, yang
artinya jumlah panggilan bergantung pada bagaimana scheduler kebetulan berjalan —
dan ada kelas bug yang hanya muncul di bawah beban. Di sini jumlah
panggilan adalah property dari bentuk query.

Tiga hal yang wajib dijaga setiap batch resolver, dan ketiganya diuji:

1. **Panjang hasil harus sama dengan `len(objs)`.** `gqlgen` mengindex berdasarkan
   posisi; slice yang lebih pendek membuat marshaler panic, yang lebih panjang
   menggeser setiap hasil ke parent yang salah.
2. **Kegagalan tidak boleh mengembalikan error.** `Product.category` nullable;
   mengembalikan error akan membuat satu `Product` null, dan karena `Product` adalah
   elemen non-null dari list non-null, itu merambat sampai `products: null`.
   Satu dependency mati menghapus seluruh halaman. Errors array tetap diisi, karena
   null yang diam-diam tidak bisa dibedakan dari kategori yang memang tidak ada.
3. **Urutan harus sesuai.** Batch yang mengembalikan hasil dalam urutan berbeda
   menghasilkan halaman di mana setiap produk menampilkan kategori yang masuk akal
   tetapi salah — bug terburuk di kelas ini karena tidak terlihat.

### Batch resolver: field yang tidak bisa di-batch

Tidak semua field bisa mengikuti aturan di atas, dan ada satu yang perlu dijelaskan
karena bentuk kodenya terlihat mendukungnya tetapi tidak.

`Media.downloadUrl` adalah kandidat batch resolver yang wajar. Batch resolver akan
memanggilnya sekali untuk seluruh list, dan hasilnya benar — itu yang membuat bug ini
sulit dilihat. Masalahnya bukan di situ: yang tetap terjadi adalah satu panggilan **per
produk**.

Penyebabnya bentuk return type, bukan konfigurasi. `gqlgen` menggabungkan batch resolver
dengan mendaftarkan parent saat ia marshal sebuah list. `Product.primaryMedia`
mengembalikan satu `Media` per produk, jadi setiap record itu di-marshal sendirian dan
`Media.downloadUrl` di-resolve satu per record. Root-nya `graphql.BatchParentsFor`, yang
hanya berisi parent yang sedang di-marshal sebagai list — dan di sini tidak ada.

Jadi `downloadUrl` dihapus sebagai field resolver, dan signing-nya dipindahkan ke tempat
yang sudah memegang list: `signURLs` dipanggil dari dalam batch `primaryMedia`, setelah
metadata diambil, satu kali untuk seluruh halaman. `Media.downloadUrl` sekarang dibaca
langsung dari field struct.

Test yang menjaga ini ada di `graph/nplusone_test.go`, dengan call count: 20 produk harus
menghasilkan satu panggilan presign, dan mematikan `signURLs` membuatnya gagal.

`Category.ancestors`, `Category.children` dan `Category.productCount` ketiganya satu
pemanggilan tree, dan satu pemanggilan product-count.

## Kenapa `includeVariants` selalu true secara default

`ListProducts` mengembalikan variants inline, jadi meminta mereka tidak menambah round
trip. Default `true` dipilih karena product page adalah kasus umum dan memang
membutuhkan variants, dan karena tidak meminta mereka menghemat payload besar untuk
result card yang tidak butuh.

Yang membuat ini jujur adalah `Product.HasVariants` di domain model: ia
membedakan "read model tidak mengembalikan variant" dari "variant tidak diminta".
Tanpa itu, client tidak bisa tahu produk kosong dari produk yang terpotong.

## Hardening

REST client hanya bisa meminta apa yang route-nya izinkan. GraphQL client memilih
biaya sendiri per request: satu request bisa menyebut field yang sama seratus kali di
bawah alias berbeda, dan setiap kemunculan diselesaikan terpisah. Setiap limit di
bawah menjawab itu, berlapis karena masing-masing menangkap bentuk berbeda.

| Limit | Default | Menangkap |
|-------|---------|----------|
| `PARSER_TOKEN_LIMIT` | 15000 | Nested dalam, saat parse, sebelum validasi bisa menolak |
| `MAX_QUERY_LENGTH` | 16000 byte | Selection lebar dari token pendek: panjang tapi dangkal |
| `MAX_REQUEST_BODY_BYTES` | 1 MiB | Body buffering |
| `MAX_QUERY_COMPLEXITY` | 250 | Alias fan-out |
| `MAX_QUERY_DEPTH` | 12 | Recursion yang sempit tapi dalam |
| `ENABLE_INTROSPECTION` | on (image: off) | Inventaris schema gratis untuksiapa pun |
| `ENABLE_PERSISTED_QUERIES` | on | Query berulang tidak pernah melewati parser lagi |

Complexity saja tidak cukup, dan itu bukan persecution: CVE-2025-8014 adalah complexity
limit yang bisa dilewati. Depth dibatasi terpisah karena failure yang dicegah berbeda —
depth mencegah stack exhaustion, complexity mencegah request mahal. Keduanya
dilindungi `PARSER_TOKEN_LIMIT` yang berjalan lebih awal, karena nested dalam
menguras stack saat parse, bukan saat eksekusi.

Cost model di `cmd/server/main.go` punya tiga detail yang masing-masing menutup satu
bypass, dan ketiganya punya test di `cmd/server/complexity_test.go`:

- **List root dihargai dengan page size yang diminta**, bukan dengan satu. Tanpa itu,
  query untuk 1000 produk dihargai sama dengan query untuk satu — persis request
  mahal yang seharusnya tidak lolos anggaran. Harganya dibaca dari argumen pemanggil,
  jadi `limit` yang dihilangkan dihargai pada default, bukan gratis.
- **Floor satu per item.** Scalar dihargai nol, jadi list field yang seluruh
  seleksinya scalar punya child cost nol, dan nol kali enam puluh tetap nol. Alias
  fan-out atas `products { total }` akan dihargai nol berapa pun diulang,
  sementara tetap menarik enam puluh baris per alias.
- **Saturating, bukan wrap.** Overflow menghasilkan biaya kecil untuk query yang
  sangat besar — persis bypass yang ada untuk dicegah oleh sebuah limit.

Field rekursif seperti `Category.children` **sengaja tidak** diberi multiplier.
Multiplier akan mengalikan terhadap depth: dua puluh pangkat empat belas untuk query
empat belas level. Itu membuat complexity limit menolak dengan biaya sekitar 1.6e18
pada query yang seharusnya ditolak depth limit, yang membuat depth limit tidak
terjangkau untuk persis bentuk yang ada untuk membatasinya. Recursion dibatasi
`MAX_QUERY_DEPTH`, dan biaya selection bersarang tetap linear terhadap depth-nya.

Depth limiter ditulis sendiri, bukan dari dependensi. Yang direkomendasikan
(`oyyblin/gqlgen-depth-limit-extension`) commit terakhirnya Februari 2023, satu star,
dan pin ke gqlgen v0.17.24. Mengambil itu ke service yang pin v0.17.95 berarti
dependensi tak terawat menjaga satu limit yang harus benar. Sekitar 100 baris, di
`internal/handler/limits.go`, dengan test-nya sendiri.

## Error yang sampai ke client

Satu tempat memutuskan apa yang client pelajari dari kegagalan:
`errorPresenter` di `internal/handler/graphql.go`. Dan itu sempit secara sengaja —
sebuah error type baru terlihat hanya dengan ditambahkannya di sana, bukan bocor
secara default.

Klasifikasi gRPC dipetakan ke kosakata kecil di `Classify`, dan arah defaultnya
penting: **kode yang tidak dikenal menjadi retryable**. Menebak "client error" akan
menghentikan retry untuk fault yang belum ada yang mendiagnosis; menebak "retryable"
biaya paling banyak beberapa percobaan sia-sia.

| Situasi | Code | Retryable |
|---------|------|-----------|
| reference tidak ada | `NOT_FOUND` | — |
| input tidak valid | `BAD_USER_INPUT` | — |
| conflict | `CONFLICT` | — |
| dependency tidak bisa dihubungi | `DEPENDENCY_UNAVAILABLE` | true |
| melewati limit | `COMPLEXITY_LIMIT_EXCEEDED`, `DEPTH_LIMIT_EXCEEDED`, `QUERY_TOO_LONG`, `GRAPHQL_PARSE_FAILED` | — |
| bug | `INTERNAL` | — |

Detail internal tidak pernah sampai ke client. gRPC error memuat alamat target dan
wrapped Go error memuat package path; keduanya hanya ke log. Ada test yang memeriksa
ini, karena kebocoran topologi internal lewat pesan error adalah hal yang mudah
terjadi dan sulit noticed.

## Timeout

Tiga lapis, dan urutannya penting.

1. **`REQUEST_TIMEOUT` (10s)** — deadline di request context, jadi ia mencapai setiap
   panggilan downstream tanpa setiap resolver tahu. Fan-out 20 field ke 5
   service punya 20 panggilan, dan jumlah budget individual mereka bukan batas untuk
   apa pun. Tanpa ini, kumpulan panggilan lambat yang belum timeout menahan request
   sampai client menyerah, dan aggregator menumpuknya.
2. **`DEPENDENCY_TIMEOUT` (3s)** — per panggilan. Divalidasi lebih kecil dari
   `REQUEST_TIMEOUT` saat startup, karena kalau tidak per panggilan tidak melakukan
   apa-apa dan satu dependency lambat bisa menahan request selama seluruh budget.
3. **Readiness timeout** — untuk probe dependency.

`withTimeout` tidak pernah melonggarkan deadline yang lebih ketat. Budget yang sudah
terpakai menghasilkan context yang sudah dibatalkan, karena memulai panggilan yang
tidak bisa selesai lebih tidak jujur daripada tidak memulainya.

### Transport: fail-fast, bukan `WaitForReady`

Koneksi gRPC sengaja **tidak** memakai `WaitForReady(true)`, padahal itu
pengaturan pertama yang biasanya diambil client yang ingin tahan banting. Alasannya
terukur, bukan preferensi.

`WaitForReady(true)` menahan panggilan yang gagal sampai context-nya habis, sehingga
koneksi yang ditolak berubah menjadi tunggu sepanjang timeout. Diukur pada peer mati
dengan budget per percobaan 3s: **3s dengan `WaitForReady`, 0s tanpa**. Dependency yang
mati membuat setiap percobaan seharga timeout penuh, lalu retry loop mengalikannya
dengan jumlah percobaan.

Yang lebih buruk, ini merusak circuit breaker. Breaker baru melihat kegagalan setelah
percobaan benar-benar kembali, jadi kegagalan 3s per percobaan berarti perlu **45s
lalu lintas** trafik sebelum breaker dengan ambang lima kegagalan beruntun terbuka — dan
45s itu adalah seluruh insidennya. Tanpa `WaitForReady`, breaker yang sama terbuka
dalam waktu jauh di bawah satu detik.

Yang dibeli `WaitForReady` adalah toleransi reconnect singkat saat rolling restart, dan
retry loop sudah memberi itu: `Unavailable` diklasifikasi retryable, jadi percobaan
kedua akan mendial ulang dan berhasil setelah peer kembali. Retry adalah mekanisme yang
lebih baik di sini karena ia juga merekam apa yang terjadi — satu span event per
percobaan, satu laporan ke breaker, satu baris log — sedangkan `WaitForReady`
menyembunyikan reconnect di dalam satu panggilan buram.

Test yang menjaganya ada di `internal/grpcclient/transport_test.go` dan melakukan dial
melalui `New()`, bukan lewat fake, karena yang diuji adalah option pada koneksi yang
dibuat `New`. Menghidupkan kembali `WaitForReady` membuatnya gagal dengan angka yang
sama: 9s per request, 45s untuk breaker terbuka.

## Health

Liveness dan readiness berbeda secara sengaja, dan perbedaannya bukan detail.

- `/health`, `/healthz` — **menyentuh tidak ada dependency.** Probe liveness yang
  memanggil dependency akan membuat orchestrator me-restart pod setiap kali dependency
  berkedip, mengubah outage sebagian jadi total.
- `/readyz` — memeriksa keenam dependency secara konkuren. Kubernetes memakai
  keduanya berbeda: liveness gagal berarti pod dibunuh, readiness gagal berarti pod
  dikeluarkan dari load balancer. Jadi kegagalan dependency harus menghentikan
  instance ini mengambil request baru, sambil tetap hidup untuk melayani yang bisa.

Probe readiness dijalankan konkuren karena independen, dan sequential akan membuat
durasi probe jadi jumlah seluruhnya — cukup panjang untuk menjatuhkan timeout probe
nya sendiri tepat saat outage sebagian yang harusnya terdeteksi.

## Kontrak yang dipakai

Proto di `proto/` adalah salinan kontrak service pemiliknya dengan hanya `go_package`
yang ditulis ulang. Duplikasi itu disengaja dan sama dengan yang dilakukan
`product-service` untuk category dan media: sebuah peer harus bisa build dan release
tanpa perubahan terkoordinasi ke pemilik kontrak. Biayanya, perubahan kontrak muncul
sebagai decode error di response, bukan sebagai compile error. Lima service lain di repo
ini membuat trade yang sama.

| Contract | Dipakai untuk |
|----------|--------------|
| `category.v1.CategoryService` | category, tree, batch by id |
| `media.v1.MediaService` | metadata, `BatchGetDownloadURLs` |
| `product.v1.ProductService` | writes saja |
| `productlisting.v1.ProductListingService` | browse reads |
| `productsearch.v1.ProductSearchService` | search reads |

Dua hal yang tidak diekspos gRPC dan jadi batasan nyata:

- **Category tidak punya write RPC.** `category.v1` hanya expose baca, dengan
  deserialize: hanya service pemiliknya yang menulis tabelnya. Query dan mutation
  category di schema menjawab dependency-unavailable dengan pesan yang menyebut
  REST API. Ini dilaporkan sebagai unavailable, bukan invalid input — request-nya
  benar, kemampuannya tidak ada di transport ini.
- **Projection status hanya REST.** Kedua read service tidak punya status RPC. Itu
  sebabnya `internal/projection` satu-satunyakolaborator yang bicara HTTP, dan
  kesadarannya terkumpul di satu interface alih-alih tersebar di gRPC clients.

`internal/projection` juga decoding ke dua struct terpisah (`products` vs
`documents`), supaya perubahan kontrak gagal keras di decode time, bukan diam-diam
membaca nol.

## Configuration

Semua dari environment; default ada di setiap baris dan Startup gagal pada
konfigurasi yang tidak bisa bekerja, bukan pada request pertama yang membutuhkannya.

| Variabel | Default | Catatan |
|----------|---------|---------|
| `APP_PORT` | 8085 | |
| `CATEGORY_GRPC_TARGET` | localhost:9090 | |
| `MEDIA_GRPC_TARGET` | localhost:9091 | |
| `PRODUCT_GRPC_TARGET` | localhost:9092 | |
| `LISTING_GRPC_TARGET` | localhost:9093 | |
| `SEARCH_GRPC_TARGET` | localhost:9094 | |
| `LISTING_HTTP_BASE` | http://localhost:8083 | status saja |
| `SEARCH_HTTP_BASE` | http://localhost:8084 | status saja |
| `REQUEST_TIMEOUT` | 10s | harus > `DEPENDENCY_TIMEOUT` |
| `DEPENDENCY_TIMEOUT` | 3s | per panggilan |
| `MAX_QUERY_COMPLEXITY` | 250 | |
| `MAX_QUERY_DEPTH` | 12 | |
| `MAX_QUERY_LENGTH` | 16000 | byte |
| `PARSER_TOKEN_LIMIT` | 15000 | |
| `MAX_REQUEST_BODY_BYTES` | 1048576 | |
| `ENABLE_INTROSPECTION` | true | image Docker set false |
| `ENABLE_PLAYGROUND` | false | mengeksekusi query nyata |
| `ENABLE_PERSISTED_QUERIES` | true | |
| `ALLOWED_ORIGINS` | kosong | `*` untuk wildcard; request tanpa Origin selalu diizinkan |
| `SHUTDOWN_TIMEOUT` | 30s | |

Boolean menerima `1/true/yes/on` dan `0/false/no/off`. `strconv.ParseBool`
menolak `yes` dan `on`, dan keduanya muncul di compose file nyata.

`LOG_LEVEL=debug` dengan `ENABLE_INTROSPECTION=false` ditolak saat startup: operator
yang sedang debug schema hampir selalu butuh introspection, dan kombinasi itu hampir
selalu salah konfigurasi.

## Development

```bash
go build ./...
go test ./...
go test -race ./...
go test -cover ./...
go run ./cmd/server
```

Test tidak butuh database, Kafka, MinIO, atau service lain. Setiap layer mendeklarasikan
interface untuk dependency-nya, jadi semuanya diuji dengan stub — termasuk static
assertion di `internal/grpcclient/adapt.go` yang memaksa setiap adapter memenuhi port
yang diklaimnya.

Test yang paling bernilai di sini bukan yang menguji nilai, tapi yang menguji
**jumlah panggilan**: `TestProductPageResolvesInThreeCalls` dan
`TestProductCategoryIsResolvedInOneCall`. Menghapus batch resolver dan menggantinya
dengan single-object resolver akan membuat semua test fungsional tetap lolos —
response-nya identik — dan hanya muncul sebagai load berlipat di category-service
dan media-service saat trafik produksi. Karena itu assertion-nya tentang call count,
bukan hanya tentang isi.

`graph/nplusone_test.go` menambahkannya di level yang lebih tinggi: ia menjalankan query
sungguhan lewat executor lalu menghitung panggilan, dan mengulanginya pada lebar yang
berbeda (`1`, `20`, `50`) untuk membuktikan jumlahnya tidak tumbuh bersama halaman. Itu
yang menangkap bug `downloadUrl` yang mana test lain akan lolos — hasilnya benar,
hanya biayanya berlipat.

Tiga di antaranya diverifikasi sebagai regression test: mematikan `signURLs`,
mematikan memo `RequestCache`, dan melepas middleware-nya dari harness masing-masing
membuat test yang tepat gagal.

### Regenerasi

```bash
./hack/gen.sh
```

Menjalankan dua generator: stub protobuf dari `proto/`, lalu kode GraphQL dari
`graph/schema.graphql`. Keluarannya di-commit.

Generation adalah host step, bukan Bazel action. gqlgen menyelesaikan tipe Go dengan
mengeksekusi toolchain Go lewat `go/packages`, yang tidak bekerja di sandbox Bazel, dan
upstream tidak punya fix yang sudah landed
([99designs/gqlgen#3181](https://github.com/99designs/gqlgen/issues/3181), masih
terbuka). `skip_validation` dan `skip_mod_tidy` aktif karena itu, dan autobind
dimatikan karena setiap tipe di-bind eksplisit dengan `@goModel`. `go build` dan
`bazel build` tetap mengompilasi hasilnya, jadi generation yang rusak gagal di build
dan bukan lolos ke produksi.

**Badan resolver hidup di `graph/schema.resolvers.go`, dan file itu milik gqlgen.**
`layout: follow-schema` menulis seluruh badan resolver ke sana. Konsekuensinya nyata:
memindahkan badan resolver ke file lain akan menghasilkan deklarasi ganda pada
run berikutnya, dan mengedit `schema.resolvers.go` secara manual akan hilang begitu
`gen.sh` dijalankan. Semua penjelasan desain yang perlu bertahan ada di file-file
hand-written: `resolvers_common.go` (helper bersama, tracing, memo per request) dan
`resolvers_boundary.go` (konversi tipe boundary antara GraphQL dan kontrak).

Satu pengecualian: `resolver_types.go` pernah ada untuk accessor resolver, tapi gqlgen juga
menghasilkannya sendiri, jadi accessor itu kini milik file generated. Helper tracing
dipindah ke `*Resolver` sendiri karena tipe receiver di-regenerate pada setiap
codegen run, sementara method di root dipromosikan lewat embedded pointer dan bertahan.

Butuh:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Contoh

```bash
docker compose up -d --build
curl localhost:8085/readyz
```

Query satu produk lengkap dengan kategori, breadcrumb, gambar dan variant:

```bash
curl -s localhost:8085/query -H 'Content-Type: application/json' -d '{"query":"{
  product(id: \"1\") {
    id name priceCents
    category { name ancestors { name } }
    primaryMedia { filename downloadUrl }
    variants { sku priceCents stock }
  }
}"}' | jq
```

Browse dengan filter, satu panggilan per service:

```bash
curl -s localhost:8085/query -H 'Content-Type: application/json' -d '{"query":"{
  products(filter: { status: ACTIVE, limit: 5 }) {
    total
    products { id name priceCents category { name } primaryMedia { downloadUrl } }
  }
}"}' | jq
```

Search, yang read model-nya berbeda:

```bash
curl -s localhost:8085/query -H 'Content-Type: application/json' \
  -d '{"query":"{ searchProducts(filter: { query: \"aurora\" }) { total tookMs hits { name score } } }"}' | jq
```

Seberapa jauh kedua proyeksi tertinggal:

```bash
curl -s localhost:8085/query -H 'Content-Type: application/json' \
  -d '{"query":"{ projectionHealth { listing { records appliedEvents healthy } search { records appliedEvents healthy } } }"}' | jq
```

Create, lalu perhatikan `projected: false`:

```bash
curl -s localhost:8085/query -H 'Content-Type: application/json' -d '{"query":"mutation {
  createProduct(input: {
    categoryId: \"1\", name: \"Aurora Phone 15 Pro\",
    priceCents: 12999000, stock: 25, status: ACTIVE
  }) { projected appliedEvents product { id name } }
}"}' | jq
```

Upload dua langkah, langsung ke object storage:

```bash
# 1. daftar, dapat presigned target
curl -s localhost:8085/query -H 'Content-Type: application/json' -d '{"query":"mutation {
  registerMedia(input: { ownerId: \"user-1\", filename: \"red.png\", contentType: \"image/png\" }) {
    media { id status }
    upload { url method headers { name value } expiresAt }
  }
}"}' | jq
# 2. PUT ke URL itu, lalu
curl -s localhost:8085/query -H 'Content-Type: application/json' \
  -d '{"query":"mutation { completeMediaUpload(id: \"1\") { id status downloadUrl } }"}' | jq
```

## Batasan yang diketahui

**Category write tidak tersedia lewat GraphQL.** Kontrak gRPC-nya read-only
secara sengaja, jadi ketiga mutation category menjawab
`DEPENDENCY_UNAVAILABLE` dengan pesan yang menyebut REST API. Menghilangkan ini
butuh perubahan di `category-service`: menambah write RPC, atau addendum ke kontrak
yang ada. Yang kedua lebih murah dan tidak mengubah aturan authority yang ada —
`category-service` memang read-only lewat gRPC supaya hanya pemiliknya yang menulis.
Perlu diputuskan, bukan diasumsikan.

**`Category.productCount` tidak akurat pada katalog besar.**
Kontrak listing tidak punya count-by-group RPC, jadi `CountProductsByCategory`
mengambil satu halaman lalu menjumlahkannya secara lokal. Itu satu round trip,
yang merupakan properti pentingnya — alternatifnya satu `ListProducts` per kategori, yang akan
mengubah field opsional pada category menjadi amplifier. Tapi sample-nya berbatas: kalau
katalog lebih besar dari satu halaman, total sebenarnya tidak diketahui dan **setiap**
id dikembalikan kosong, yang dirender sebagai null. Mengembalikan hasil sample
akan terlihat seperti jawaban dan akan salah justru pada katalog besar tempat count
paling berguna. Pada titik itu ini butuh count-by-group RPC di service listing.

**GET transport diaktifkan.** Default upstream mati karena query di URL masuk ke access
log, proxy log, dan browser history. Di sini ia berguna untuk read-only health check
dan client yang tidak bisa POST. Jika aggregator ini jadi publik, matikan.

**Belum ada authentication.** Query dan mutation terbuka. Service ini mewarisi
keterbatasan ini dari lima service di bawahnya, yang juga tidak punya auth; tetapi
composition layer adalah tempat yang paling logis untuk menambahkannya, karena satu
titik menutup semua API. Mekanisme tokennya belum diputuskan.

**Trace hanya mencakup sisi aggregator.** Kelima service di bawah tidak mengekstrak
`traceparent`, jadi sebuah trace punya satu span HTTP dan span client untuk setiap
panggilan gRPC, tanpa apa pun di bawahnya. Itu jujur tentang pandangan service ini
dan diam tentang semua yang terjadi melewati kabel.

Untuk pertanyaan yang biasanya diajukan ke trace — dependency mana yang lambat, field
mana yang degradasi, berapa percobaan yang sebuah panggilan butuhkan, apakah breaker
terbuka saat panggilan ditolak — itu cukup. Yang tidak cukup adalah melihat apa yang
dilakukan dependency terhadap request, dan konfigurasi apa pun di sini tidak akan
mengubahnya. Saat lima service itu diinstrumentasi, trace ID yang sama akan langsung
berisi child span tanpa perlu perubahan di sisi ini.

Propagator global tetap dipasang bahkan ketika tracing dimatikan, sehingga
`traceparent` tetap dikirim. Kalau tidak, propagasi diam-diam menjadi no-op: trace ID
tidak akan cocok di mana pun, tanpa warning.

**Tidak ada endpoint metrik.** Retry, breaker, dan tracing di sini punya log dan span
tetapi tidak punya counter yang bisa di-scrape. Pertanyaan "berapa trafik yang kita
shed" dijawab lewat span `breaker.refused`, yang berarti butuh trace backend, bukan
prometheus. Micrometer atau OTLP metrics akan menutup celah ini; belum ditambahkan
karena belum ada konsumen yang jelas, dan exporter yang tidak dipakai hanya menambah
permukaan kegagalan.

**Tidak ada span per-field untuk `Media.downloadUrl`.** `downloadUrl` sekarang dibaca
dari field struct, jadi tidak punya resolver dan tidak punya span. URL-nya ditimestamp
di dalam span `Product.primaryMedia`, yang tetap mencatat `ecommerce.dependents`.
Timeout per-field tidak mungkin tanpa perubahan schema.

## License

MIT
