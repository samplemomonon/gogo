package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gogo/internal/shared/netutil"
)

// Validate checks the loaded configuration for logical consistency and
// well-formedness. It returns all validation errors joined together so
// operators see the complete picture at startup rather than fixing one
// issue at a time.
func (c *AppConfig) Validate() error {
	var errs []error

	if port, err := strconv.Atoi(c.Port); err != nil || port < 1 || port > 65535 {
		errs = append(errs, fmt.Errorf("PORT %q must be a valid TCP port", c.Port))
	}

	for _, timeout := range []struct {
		name string
		dur  time.Duration
	}{
		{"READ_TIMEOUT", c.ReadTimeout},
		{"WRITE_TIMEOUT", c.WriteTimeout},
		{"IDLE_TIMEOUT", c.IdleTimeout},
		{"REQUEST_TIMEOUT", c.RequestTimeout},
		{"SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
		{"READ_HEADER_TIMEOUT", c.ReadHeaderTimeout},
	} {
		if timeout.dur <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", timeout.name, timeout.dur))
		}
	}

	if c.SlowRequest < 0 {
		errs = append(errs, fmt.Errorf("SLOW_REQUEST_MS must not be negative, got %s", c.SlowRequest))
	}

	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be \"json\" or \"text\", got %q", c.LogFormat))
	}

	switch c.AppEnv {
	case "development", "staging", "production", "test":
	default:
		errs = append(errs, fmt.Errorf("APP_ENV must be one of development, staging, production, test; got %q", c.AppEnv))
	}

	if c.MaxHeaderBytes < 0 {
		errs = append(errs, fmt.Errorf("MAX_HEADER_BYTES must not be negative, got %d", c.MaxHeaderBytes))
	}
	if c.MaxBodyBytes < 0 {
		errs = append(errs, fmt.Errorf("MAX_BODY_BYTES must not be negative, got %d", c.MaxBodyBytes))
	}

	// The trusted-proxy set is parsed by the same helper the middleware uses
	// to resolve client IPs, so validation and runtime behavior cannot drift.
	if _, err := netutil.ParseTrustedProxies(c.TrustedProxies); err != nil {
		errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
	}

	if strings.TrimSpace(c.ServiceName) == "" {
		errs = append(errs, errors.New("SERVICE_NAME must not be empty"))
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		errs = append(errs, errors.New("APP_ENV must not be empty"))
	}

	if c.DBMaxConns < 1 {
		errs = append(errs, fmt.Errorf("DB_MAX_CONNS must be at least 1, got %d", c.DBMaxConns))
	}
	if c.DBMinConns < 0 {
		errs = append(errs, fmt.Errorf("DB_MIN_CONNS must not be negative, got %d", c.DBMinConns))
	}
	if c.DBMinConns > c.DBMaxConns {
		errs = append(errs, fmt.Errorf("DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)", c.DBMinConns, c.DBMaxConns))
	}
	if c.DBPingTimeout <= 0 {
		errs = append(errs, fmt.Errorf("DB_PING_TIMEOUT must be positive, got %s", c.DBPingTimeout))
	}

	// Every product-platform service is required. The gateway owns no part of the
	// product domain any more (ARCHITECTURE.md D-018), so a missing address is not a
	// "run it in-process instead" signal — there is nothing to run in-process. Failing
	// here is what stops the gateway from booting into a storefront that is
	// permanently, silently empty.
	for _, upstream := range []struct {
		name    string
		address string
	}{
		{"PRODUCT_ADDRESS", c.ProductAddress},
		{"PRODUCT_LISTING_ADDRESS", c.ProductListingAddress},
		{"PRODUCT_SEARCH_ADDRESS", c.ProductSearchAddress},
		{"CATEGORY_ADDRESS", c.CategoryAddress},
	} {
		address := strings.TrimSpace(upstream.address)
		if address == "" {
			errs = append(errs, fmt.Errorf("%s must be set: the gateway requires every product-platform service", upstream.name))
			continue
		}
		// A gRPC target is "host:port" or a "scheme://authority" form such as
		// "dns:///service.namespace.svc.cluster.local:9092". Rejecting an address with
		// neither a scheme nor a host:port pair catches the common typo of pointing at
		// an HTTP URL or at a bare hostname, both of which would otherwise fail as an
		// opaque dial error at first request.
		if !strings.Contains(address, "://") && !strings.Contains(address, ":") {
			errs = append(errs, fmt.Errorf("%s %q must be host:port or a gRPC target URL", upstream.name, upstream.address))
		}
	}
	if len(strings.TrimSpace(c.ProductPlatformServiceToken)) < 16 {
		errs = append(errs, errors.New("PRODUCT_PLATFORM_SERVICE_TOKEN must be set and contain at least 16 characters"))
	}

	// The change feed's brokers are optional — with none, the gateway serves on cache
	// TTLs alone — but each one that *is* configured must be host:port, so a typo fails
	// at startup instead of silently subscribing to nothing.
	for _, broker := range c.KafkaBrokers {
		if !strings.Contains(broker, ":") {
			errs = append(errs, fmt.Errorf("KAFKA_BROKERS entry %q must be host:port", broker))
		}
	}

	// Tenant declaration: no host or explicit id may be blank, so a website
	// can never silently lose its tenant. An empty default is valid and means
	// the built-in "default" tenant.
	for host, id := range c.Tenant.Hosts {
		if strings.TrimSpace(host) == "" {
			errs = append(errs, errors.New("TENANT_HOSTS contains an empty host"))
		}
		if strings.TrimSpace(id) == "" {
			errs = append(errs, fmt.Errorf("TENANT_HOSTS host %q maps to an empty tenant", host))
		}
	}
	for _, id := range c.Tenant.IDs {
		if strings.TrimSpace(id) == "" {
			errs = append(errs, errors.New("TENANT_IDS contains an empty tenant"))
		}
	}

	// Media module validation
	if c.Media.MaxFileSize <= 0 {
		errs = append(errs, fmt.Errorf("MEDIA_MAX_FILE_SIZE must be positive, got %d", c.Media.MaxFileSize))
	}
	if len(c.Media.AllowedMimeTypes) == 0 {
		errs = append(errs, errors.New("MEDIA_ALLOWED_MIME_TYPES must not be empty"))
	}
	if strings.TrimSpace(c.Media.StoragePath) == "" {
		errs = append(errs, errors.New("MEDIA_STORAGE_PATH must not be empty"))
	}

	return errors.Join(errs...)
}
