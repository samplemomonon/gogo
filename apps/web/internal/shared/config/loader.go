package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"gogo/internal/shared/version"
)

// LoadValidated loads configuration, validates it, and applies the build-time
// version override when the environment left ServiceVersion at "dev" but the
// build metadata indicates a release. Every binary — the gateway and the
// standalone services — uses this entry point, so validation policy cannot
// drift between them.
func LoadValidated() (*AppConfig, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.ServiceVersion == "dev" && version.Version != "dev" {
		cfg.ServiceVersion = version.Version
	}
	return cfg, nil
}

// Load reads configuration from the environment. A .env file is loaded
// first if present (missing files are ignored). Unlike a silent-fallback
// loader it fails loudly on malformed values, so a typo in a timeout or a
// bad CIDR cannot slip into production; callers should abort with the
// returned error instead of continuing with partial configuration.
func Load() (*AppConfig, error) {
	// .env is optional; ignore the error when absent.
	_ = godotenv.Load()

	logFormat := envString("LOG_FORMAT", "")
	if logFormat == "" {
		if envString("APP_ENV", "development") == "production" {
			logFormat = "json"
		} else {
			logFormat = "text"
		}
	}

	appEnv := envString("APP_ENV", "development")

	trustedProxies := []string{}
	if raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if cidr := strings.TrimSpace(entry); cidr != "" {
				trustedProxies = append(trustedProxies, cidr)
			}
		}
	}

	var errs []error
	logLevel, err := parseLogLevel(envString("LOG_LEVEL", "info"))
	if err != nil {
		errs = append(errs, err)
	}

	loadDuration := func(key string, fallback time.Duration) time.Duration {
		duration, set, err := envDuration(key)
		if err != nil {
			errs = append(errs, err)
			return fallback
		}
		if !set {
			return fallback
		}
		// A value the operator set explicitly always wins — including zero,
		// which Validate then rejects for required timeouts. Only an unset
		// variable falls back to the default.
		return duration
	}

	cfg := &AppConfig{
		AppEnv: appEnv,
		Host:   envString("HOST", "0.0.0.0"),
		Port:   envString("PORT", "8080"),

		ServiceName:    envString("SERVICE_NAME", "gogo"),
		ServiceVersion: envString("SERVICE_VERSION", "dev"),

		LogLevel:  logLevel,
		LogFormat: logFormat,

		ReadTimeout:       loadDuration("READ_TIMEOUT", 5*time.Second),
		WriteTimeout:      loadDuration("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:       loadDuration("IDLE_TIMEOUT", 60*time.Second),
		RequestTimeout:    loadDuration("REQUEST_TIMEOUT", 15*time.Second),
		ShutdownTimeout:   loadDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		ReadHeaderTimeout: loadDuration("READ_HEADER_TIMEOUT", 5*time.Second),
		SlowRequest:       loadSlowRequest(&errs),

		MaxHeaderBytes: envInt("MAX_HEADER_BYTES", 1<<20, &errs),
		MaxBodyBytes:   envInt64("MAX_BODY_BYTES", 1<<20, &errs),

		TrustedProxies: trustedProxies,

		EnableDebugEndpoint: envBool("ENABLE_DEBUG_ENDPOINT", appEnv != "production", &errs),

		DatabaseURL:       envString("DATABASE_URL", ""),
		DBMaxConns:        envInt("DB_MAX_CONNS", 10, &errs),
		DBMinConns:        envInt("DB_MIN_CONNS", 1, &errs),
		DBMaxIdleTime:     loadDuration("DB_MAX_IDLE_TIME", 5*time.Minute),
		DBPingTimeout:     loadDuration("DB_PING_TIMEOUT", 5*time.Second),
		DBMaxConnLifetime: loadDuration("DB_MAX_CONN_LIFETIME", 30*time.Minute),

		ProductAddress:              envString("PRODUCT_ADDRESS", ""),
		ProductListingAddress:       envString("PRODUCT_LISTING_ADDRESS", ""),
		ProductSearchAddress:        envString("PRODUCT_SEARCH_ADDRESS", ""),
		CategoryAddress:             envString("CATEGORY_ADDRESS", ""),
		MediaAddress:                envString("MEDIA_ADDRESS", ""),
		ProductPlatformServiceToken: envString("PRODUCT_PLATFORM_SERVICE_TOKEN", ""),
		BuilderPublicAPIKey:         envString("BUILDER_PUBLIC_API_KEY", ""),
		BuilderModel:                envString("BUILDER_CONTENT_MODEL", ""),

		KafkaBrokers: loadCSV("KAFKA_BROKERS", ""),

		Tenant: loadTenantConfig(&errs),

		Media: MediaConfig{
			MaxFileSize:      envInt64("MEDIA_MAX_FILE_SIZE", 10<<20, &errs), // 10 MB default
			AllowedMimeTypes: loadAllowedMimeTypes(),
			StoragePath:      envString("MEDIA_STORAGE_PATH", "./storage/media"),
		},

		FeatureFlags: loadFeatureFlags(),
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return cfg, nil
}

// envString reads a string from the environment, trimming whitespace. An
// empty value returns the fallback.
func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// envInt parses an integer from the environment. Malformed values are
// reported to errs and the fallback is returned.
func envInt(key string, fallback int, errs *[]error) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q must be an integer", key, value))
		return fallback
	}
	return parsed
}

// envInt64 parses a 64-bit integer from the environment. Malformed values
// are reported to errs and the fallback is returned.
func envInt64(key string, fallback int64, errs *[]error) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q must be an integer", key, value))
		return fallback
	}
	return parsed
}

// envBool parses a boolean from the environment. Malformed values are
// reported to errs and the fallback is returned.
func envBool(key string, fallback bool, errs *[]error) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q must be a boolean", key, value))
		return fallback
	}
	return parsed
}

// loadSlowRequest reads SLOW_REQUEST_MS. A bare integer is interpreted as
// milliseconds (matching the variable's name); a value with a unit suffix
// (e.g. "500ms", "1s") is parsed as a duration. An unset variable uses the
// 500ms default; zero disables slow-request logging.
func loadSlowRequest(errs *[]error) time.Duration {
	const key = "SLOW_REQUEST_MS"
	const fallback = 500 * time.Millisecond

	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Duration(millis) * time.Millisecond
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s %q must be milliseconds (e.g. 500) or a duration (e.g. 500ms), got: %v", key, value, err))
		return fallback
	}
	return duration
}

// envDuration parses a duration from the environment. The returned set flag
// distinguishes "unset" from "explicitly zero", so callers can apply a
// fallback for the former while honouring (and validating) the latter. It
// returns an error for malformed values so Load can surface the
// misconfiguration instead of quietly running with defaults.
func envDuration(key string) (duration time.Duration, set bool, err error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, false, nil
	}

	duration, err = time.ParseDuration(value)
	if err != nil {
		return 0, true, fmt.Errorf("%s %q must be a duration (e.g. 5s, 500ms), got: %v", key, value, err)
	}
	return duration, true, nil
}

// parseLogLevel converts a string level name to slog.Level. The empty string
// defaults to info; unrecognized names return an error.
func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		// Unset is the documented default: info.
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error; got %q", value)
	}
}

// loadTenantConfig reads the tenant declaration:
//
//   - TENANT_DEFAULT: the tenant an unmatched host resolves to ("default").
//   - TENANT_HOSTS: comma-separated "host=tenant" pairs mapping a website's
//     host to its tenant (e.g. "shop.example.com=ecommerce").
//   - TENANT_IDS: comma-separated tenants served without a host (API-only
//     clients); optional, derived from default + hosts when empty.
//   - TENANT_TRUST_HEADER: honor the X-Tenant-ID header (off by default).
//
// Malformed host entries are reported to errs so a typo cannot silently drop a
// website's tenant.
func loadTenantConfig(errs *[]error) TenantConfig {
	defaultID := envString("TENANT_DEFAULT", "default")

	hosts := make(map[string]string)
	if raw := strings.TrimSpace(os.Getenv("TENANT_HOSTS")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			host, id, ok := strings.Cut(entry, "=")
			host = strings.TrimSpace(host)
			id = strings.TrimSpace(id)
			if !ok || host == "" || id == "" {
				*errs = append(*errs, fmt.Errorf("TENANT_HOSTS entry %q must be host=tenant", entry))
				continue
			}
			hosts[host] = id
		}
	}

	ids := []string{}
	if raw := strings.TrimSpace(os.Getenv("TENANT_IDS")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if id := strings.TrimSpace(entry); id != "" {
				ids = append(ids, id)
			}
		}
	}

	return TenantConfig{
		DefaultID:   defaultID,
		Hosts:       hosts,
		IDs:         ids,
		TrustHeader: envBool("TENANT_TRUST_HEADER", false, errs),
	}
}

// loadCSV reads a comma-separated environment variable into a slice, dropping
// blank entries. An unset or empty value returns nil.
func loadCSV(key, fallback string) []string {
	raw := envString(key, fallback)
	if raw == "" {
		return nil
	}
	var values []string
	for _, entry := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(entry); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// loadAllowedMimeTypes reads MEDIA_ALLOWED_MIME_TYPES as a comma-separated
// list. Defaults to common image types.
func loadAllowedMimeTypes() []string {
	const defaultTypes = "image/jpeg,image/png,image/gif,image/webp"
	raw := envString("MEDIA_ALLOWED_MIME_TYPES", defaultTypes)

	var types []string
	for _, entry := range strings.Split(raw, ",") {
		if mimeType := strings.TrimSpace(entry); mimeType != "" {
			types = append(types, mimeType)
		}
	}
	return types
}

// loadFeatureFlags reads FEATURE_FLAGS as a comma-separated list of flag names
// (e.g., "flag1,flag2"). All listed flags are enabled; unlisted flags are disabled.
func loadFeatureFlags() map[string]bool {
	raw := envString("FEATURE_FLAGS", "")
	flags := make(map[string]bool)

	if raw == "" {
		return flags
	}

	for _, entry := range strings.Split(raw, ",") {
		if flag := strings.TrimSpace(entry); flag != "" {
			flags[flag] = true
		}
	}
	return flags
}
