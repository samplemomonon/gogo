// Package bootstrap holds the gateway's startup steps: the signal-aware root context, the
// connection to the product platform (the four services that own the product domain), the
// optional product change feed, and the gateway composition (BuildGateway).
//
// The configuration half — the pages' ports, the in-process modules, the shared bus and
// its kernel view — lives in the DI container in di.go. This package is the part that has
// to *do* something rather than declare a provider: dialing, starting a background feed,
// and handing back one shutdown hook.
//
// The bootstrap package is organized into focused files:
//   - signal.go: signal handling for graceful shutdown
//   - module_gateway.go: gateway application composition
//   - product_connection.go: product-platform connections and the change feed
//   - inventory_connection.go / sales_connection.go / auth_connection.go: the modules
//     this process still owns, each mounted over an in-process gRPC transport
package bootstrap
