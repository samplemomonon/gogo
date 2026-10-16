# internal/pages

Tempat page/use-case composition: permukaan HTTP top-level gateway yang
BUKAN module. Halaman di sini tidak punya manifest, tidak punya lifecycle
(Lifer/Starter/Stopper/HealthReporter), dan tidak ikut masuk
`/module-registry`.

```
internal/pages/
├── home/     # storefront publik — GET / (read-only listing)
├── seller/   # konsol seller — /seller (produk, kategori, supplier, stok, kasir, transaksi)
├── auth/     # halaman masuk — /login + /logout (konsumsi auth.v1)
└── profile/  # halaman akun — /profile (pesanan, alamat, pengaturan)
```

## Apa bedanya dengan module?

| Module                                | Page                          |
| ------------------------------------- | ----------------------------- |
| Punya `module.Descriptor` (manifest)  | Tidak ada manifest            |
| `RegisterRoutes` + lifecycle + health | Hanya `RegisterRoutes`        |
| Terdaftar di `module.Registry`        | Disusun di composition root   |
| Nav dari manifest (`Registry.Nav()`)  | Nav via method `Nav()`        |
| Bisa jadi layanan mandiri             | Selalu bagian dari gateway    |

Gateway mengompilasi nav shell dari **page dulu, lalu module**
(`Registry.Nav()`), sehingga entri Home lalu Seller selalu memimpin top bar
di depan item hasil federasi module.

## Aturan penempatannya

1. **Jangan menempatkan business module di sini.** `internal/modules/*`
   tetap satu-satunya tempat fitur yang bisa dideploy mandiri.
2. Page seperti home/seller mengonsumsi module **sebagai remote consumer**
   lewat kontrak gRPC-nya (`infrastructure/*client` di atas kit
   `internal/shared/*consumer`), over koneksi jaringan untuk service terpisah
   atau transport in-process (bufconn) untuk monolit — sama seperti klien
   eksternal; tidak pernah import kode runtime module.
3. `internal/pages/*/application/results` dan `application/ports` adalah read
   model + kontrak page itu sendiri, bukan milik team module. auth dan
   profile berbagi `shared/authconsumer` untuk transport dan error vocabulary,
   tapi masing-masing tetap punya read model sendiri.
4. Event module hanya diobservasi untuk koherensi cache island
   (`events.Subscribe` pada `catalog.product.*`), bukan untuk mempublikasi.
5. Import path page selalu `gogo/internal/pages/...`, tidak pernah
   `gogo/internal/modules/...` kecuali kontrak event.

## Wiring

Composition root (`bootstrap.BuildGateway`, dipanggil oleh
`cmd/server/main.go`) menyusun module di registry, membuka koneksi ke
catalog/listing/inventory/sales (network saat `CATALOG_ADDRESS` di-set, in-process
bufconn sebaliknya), lalu menyerahkan page ke gateway:

```go
// Satu kontrak auth dipakai dua page, jadi client-nya dipublish sekali
// lalu masing-masing page mengadaptasinya ke read model sendiri.
fdi.Provide(b, serviceAuthClient, func(c *fdi.Container) (*authconsumer.Client, error) {
    conn, _ := fdi.ResolveNamedAs[*grpc.ClientConn](c, upstreamAuth)
    authBreaker, _ := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamAuth)
    return authconsumer.New(conn, authBreaker), nil
})

// Cookie policy diputuskan sekali di composition root: Secure mengikuti
// deployment, bukan di-hard-code di handler.
fdi.ProvideType(b, func(c *fdi.Container) (session.Policy, error) {
    return session.Policy{Secure: gatewayConfig(c).IsProduction()}, nil
})

// Port tiap page di-resolve by type, jadi Register page cukup
// mendeklarasikan kapabilitas yang dia butuhkan.
fdi.ProvideType[authports.Auth](b, func(c *fdi.Container) (authports.Auth, error) {
    shared, _ := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
    return authclient.New(shared), nil
})
fdi.ProvideType[profileports.Accounts](b, func(c *fdi.Container) (profileorts.Accounts, error) {
    shared, _ := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
    return profileauthclient.New(shared), nil
})

// Tiap page/module memiliki grafnya sendiri; composition root hanya
// daftar unit.
home.Register(b)
seller.Register(b)
auth.Register(b)
profile.Register(b)
billing.Register(b)
ops.Register(b)
```

Satu `authconsumer.Client` berarti satu error vocabulary dan satu breaker
untuk kontrak yang sama — sebelumnya setiap page membangun client sendiri,
sehingga satu kontrak punya dua kosakata error dan satu breaker punya dua
set counter.

Inventory adalah source of truth stok: konsol seller mengirim penyesuaian
lewat `inventory.v1` (atomik + idempoten), bukan lewat catalog. POS mencatat
penjualan lewat `sales.v1` yang mengkomposisi `inventory.v1.AdjustStock`
untuk efek stok.
