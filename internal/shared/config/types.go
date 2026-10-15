package config

import (
	"log/slog"
	"time"
)

// AppConfig holds runtime configuration loaded from environment variables.
// Every field maps 1:1 to an environment variable so a Kubernetes
// ConfigMap/Secret injects the full configuration without surprises.
type AppConfig struct {
	AppEnv string
	Host   string
	Port   string

	// ServiceName/ServiceVersion identify the service in the module
	// registry manifest (the "shell" identity).
	ServiceName    string
	ServiceVersion string

	// LogLevel is the minimum severity to emit.
	LogLevel slog.Level

	// LogFormat is "json" (production) or "text" (development).
	LogFormat string

	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration

	// ReadHeaderTimeout bounds how long the server waits to read request
	// headers, the slowloris defense.
	ReadHeaderTimeout time.Duration

	// SlowRequest is the duration after which a request is logged as slow.
	// Zero disables slow-request logging.
	SlowRequest time.Duration

	// MaxHeaderBytes caps a single request's headers (HTTP/1 slowloris and
	// header-bomb guard). Zero means the net/http default (1 MiB).
	MaxHeaderBytes int

	// MaxBodyBytes caps a single request body. Larger bodies are rejected
	// with 413 before any handler reads them.
	MaxBodyBytes int64

	// TrustedProxies are CIDR blocks whose X-Forwarded-For / X-Real-IP
	// headers are honored when resolving the client IP. Any other source is
	// treated as untrusted (the raw RemoteAddr wins).
	TrustedProxies []string

	// EnableDebugEndpoint mounts expvar at /debug/vars. It is on in
	// development and off in production: the endpoint exposes runtime
	// internals that do not belong on a public surface.
	EnableDebugEndpoint bool

	DatabaseURL string

	// DBMaxConns and DBMinConns control the connection pool size. Zero
	// means a sensible default is chosen (see the loader).
	DBMaxConns int
	DBMinConns int

	// DBMaxIdleTime is how long an idle connection stays in the pool. Zero
	// disables idle reaping (so the pool grows to MaxConns and never shrinks).
	DBMaxIdleTime time.Duration

	// DBPingTimeout is how long to wait for a database ping during startup.
	DBPingTimeout time.Duration

	// DBMaxConnLifetime is the maximum time a connection can be reused.
	DBMaxConnLifetime time.Duration

	// ProductAddress, ProductListingAddress, ProductSearchAddress and
	// CategoryAddress are the gRPC targets of the four services that own the
	// product domain (see ARCHITECTURE.md D-018):
	//
	//	ProductAddress         product-service          the only writer of a product
	//	ProductListingAddress  product-listing-service  the browse read model
	//	ProductSearchAddress   product-search-service   the search read model
	//	CategoryAddress        category-service         the category hierarchy
	//
	// All four are required. There is no in-process fallback: the gateway no longer
	// owns any part of the product domain, so there is nothing to compose in-process.
	// A standard gRPC target works — a bare host:port, or a DNS service such as
	// "dns:///product.product.svc.cluster.local:9092".
	ProductAddress        string
	ProductListingAddress string
	ProductSearchAddress  string
	CategoryAddress       string

	// KafkaBrokers are the brokers carrying the product platform's change stream
	// (`product.events.v1`), comma-separated "host:port". When empty the gateway runs
	// with no change feed and its island caches rely on their TTL alone — a
	// development install should not need a broker to serve pages, and a gateway that
	// cannot reach the broker still serves correct (merely staler) HTML. Every entry
	// must be host:port.
	KafkaBrokers []string

	// Tenant declares the websites this process serves and how an inbound
	// host maps to one. An empty configuration serves exactly one tenant
	// named "default", so nothing changes until a second site is declared.
	Tenant TenantConfig

	// Module-specific configuration sections
	Media MediaConfig

	// FeatureFlags control optional behavior without code changes.
	FeatureFlags map[string]bool
}

// TenantConfig declares the tenants a process serves and the host → tenant
// mapping the gateway resolves requests by. It is the configuration half of
// the tenant boundary: one backend, many websites, declared not hardcoded.
type TenantConfig struct {
	// DefaultID is the tenant resolved when no host mapping matches. Load
	// always populates it (falling back to "default").
	DefaultID string

	// Hosts maps an inbound host (case-insensitive, port ignored) to a tenant
	// ID, e.g. "shop.example.com" -> "ecommerce". One host names exactly one
	// tenant.
	Hosts map[string]string

	// IDs lists every tenant this process serves, including tenants reachable
	// only by an API client (no host). When empty the served set is derived
	// from DefaultID and Hosts. The listing projection backfills every
	// declared tenant.
	IDs []string

	// TrustHeader enables honoring the X-Tenant-ID request header. It is off
	// by default: until authentication exists, only the deployment-controlled
	// Host header may select a tenant.
	TrustHeader bool
}

// MediaConfig holds configuration specific to the media module.
type MediaConfig struct {
	// MaxFileSize is the maximum allowed file size in bytes for uploads.
	MaxFileSize int64

	// AllowedMimeTypes are the MIME types accepted for upload.
	AllowedMimeTypes []string

	// StoragePath is the base directory for storing uploaded files.
	StoragePath string
}

// IsProduction returns true when APP_ENV is "production".
func (c *AppConfig) IsProduction() bool {
	return c.AppEnv == "production"
}
