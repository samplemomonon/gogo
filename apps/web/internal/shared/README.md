# shared

Shared hanya untuk sesuatu yang benar-benar generic dan memang digunakan oleh banyak module.

Paket di `internal/shared` adalah **federation substrate** — hal yang tetap berguna
ketika satu module (misal `catalog`) dihapus:

| Paket            | Kontrak yang dilayani                                   |
| ---------------- | ------------------------------------------------------- |
| `module`         | Kontrak module: `Descriptor` (manifest), `Interface`, `Registry` + endpoint JSON `/module-registry` (view HTML di gateway) |
| `events`         | Kontrak events: in-process bus `Publish`/`Subscribe` antar-module |
| `httpserver`     | Gateway: router, middleware stack, injeksi nav federated + view HTML registry |
| `grpcx`          | Transport gRPC: `Dial` (network) dan `InProcess` (bufconn) untuk koneksi swapable |
| `grpcconsumer`   | Jalur panggilan gRPC terpusat: breaker + tenant + mapping status→error; menjaga sentinel outage tidak hilang |
| `catalogconsumer`| Kit remote-client catalog: wrap catalog.v1, map wire→Core, satu kosakata error konsumen |
| `listingconsumer`| Kit remote-client listing: wrap listing.v1, map wire→Core, keadaan breaker dan sentinel konsumen |
| `authconsumer`   | Kit remote-client auth: wrap auth.v1, satu kosakata error shared oleh auth dan profile |
| `breaker`        | Circuit breaker untuk upstream gRPC yang dipakai oleh kit konsumen |
| `config`         | Konfigurasi dari environment (startup validation)         |
| `money`          | Format Rupiah (dipakai lintas view)                       |
| `web/render`     | Mekanika rendering: strategy, islands, cache — tanpa tahu catalog |
| `web/shell`      | Shell dokumen bersama: layouts + shell.Page + RegistryPage |
| `web/assets`     | CSS/JS ter-embed                                        |
| `web/pagekit`    | Page composition helpers: composer, event→invalidate, generic ListPage/PagedListPage |
| `web/session`    | Cookie session (nama, atribut, read/clear); Secure berasal dari deployment |
| `web/navigation` | Model nav shell bersama; item page lalu module, plus fakta per-request untuk active state |
| `version`        | Versi service yang di-inject linker saat build container (`-X …version.Version`), fallback ke placeholder dev |
| `foundation`     | Kernel gateway: context, DI, logger dan kontrak module     |
| `kernel`         | Primitif kernel gateway                                   |
| `idgen`          | Generator ID (UUID) di belakang port                       |
| `keyset`         | Encode/decode keyset cursor untuk path listing             |
| `logging`        | Factory logger terstruktur (service/version/env)          |
| `netutil`        | Parsing trusted proxies untuk RealIP middleware           |
| `pgxkit`         | Konstruksi pool pgx + ping untuk adapter Postgres          |
| `tenant`         | Batas tenant: ID, carrier context, registry host→tenant, middleware resolusi |

Aturan penempatan: kalau sebuah kode masih berguna saat `catalog` dihapus, ia
tidak boleh hidup di dalam `catalog`. Yang hanya dipakai satu module (misal
`httperr`, `islandkeys`, `form`) tetap tinggal di module tersebut.

## Design system

Primitive web sekarang berada di root [`design-system/web/ui`](../../../../design-system/web/ui/README.md)
sebagai Go module tersendiri. Kontrak token, kebijakan perubahan, dan release
notes berada di root [`design-system/`](../../../../design-system/README.md).
`internal/shared/web` hanya menyimpan integrasi web milik aplikasi ini
(navigation, shell, rendering, dan asset serving). Mobile dan desktop membuat
adapter native saat klien tersebut runnable. Batas ini menjaga paket private
Go tidak menjadi API palsu untuk klien lain.
