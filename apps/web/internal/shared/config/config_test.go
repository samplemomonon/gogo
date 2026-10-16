package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AppEnv != "development" {
		t.Errorf("AppEnv = %q, want development", cfg.AppEnv)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.RequestTimeout != 15*time.Second {
		t.Errorf("RequestTimeout = %s, want 15s", cfg.RequestTimeout)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want 5s", cfg.ReadHeaderTimeout)
	}
	if cfg.SlowRequest != 500*time.Millisecond {
		t.Errorf("SlowRequest = %s, want 500ms", cfg.SlowRequest)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text (development default)", cfg.LogFormat)
	}
	if !cfg.EnableDebugEndpoint {
		t.Errorf("EnableDebugEndpoint = false, want true (development default)")
	}
	if cfg.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, 1<<20)
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("APP_ENV", "production")
	t.Setenv("PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("REQUEST_TIMEOUT", "7s")
	t.Setenv("PRODUCT_ADDRESS", "dns:///product:9092")
	t.Setenv("PRODUCT_LISTING_ADDRESS", "dns:///product-listing:9093")
	t.Setenv("PRODUCT_SEARCH_ADDRESS", "dns:///product-search:9094")
	t.Setenv("CATEGORY_ADDRESS", "dns:///category:9090")
	t.Setenv("PRODUCT_PLATFORM_SERVICE_TOKEN", "test-service-token-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AppEnv != "production" {
		t.Errorf("AppEnv = %q, want production", cfg.AppEnv)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.LogLevel.String() != "DEBUG" {
		t.Errorf("LogLevel = %v, want DEBUG", cfg.LogLevel)
	}
	if cfg.RequestTimeout != 7*time.Second {
		t.Errorf("RequestTimeout = %s, want 7s", cfg.RequestTimeout)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json (production default)", cfg.LogFormat)
	}
	if cfg.EnableDebugEndpoint {
		t.Errorf("EnableDebugEndpoint = true, want false (production default)")
	}
	for _, got := range []struct {
		name    string
		address string
		want    string
	}{
		{"ProductAddress", cfg.ProductAddress, "dns:///product:9092"},
		{"ProductListingAddress", cfg.ProductListingAddress, "dns:///product-listing:9093"},
		{"ProductSearchAddress", cfg.ProductSearchAddress, "dns:///product-search:9094"},
		{"CategoryAddress", cfg.CategoryAddress, "dns:///category:9090"},
	} {
		if got.address != got.want {
			t.Errorf("%s = %q, want %q", got.name, got.address, got.want)
		}
	}

	t.Setenv("HOST", "  ")
	if cfg2, err := Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	} else if cfg2.Host != "0.0.0.0" {
		t.Errorf("blank HOST should fall back to 0.0.0.0, got %q", cfg2.Host)
	}
}

func TestLoadSlowRequest(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.

	t.Run("bare integer is milliseconds", func(t *testing.T) {
		t.Setenv("SLOW_REQUEST_MS", "500")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.SlowRequest != 500*time.Millisecond {
			t.Errorf("SlowRequest = %s, want 500ms", cfg.SlowRequest)
		}
	})

	t.Run("duration suffix is honoured", func(t *testing.T) {
		t.Setenv("SLOW_REQUEST_MS", "2s")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.SlowRequest != 2*time.Second {
			t.Errorf("SlowRequest = %s, want 2s", cfg.SlowRequest)
		}
	})

	t.Run("explicit zero disables without falling back", func(t *testing.T) {
		t.Setenv("SLOW_REQUEST_MS", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.SlowRequest != 0 {
			t.Errorf("SlowRequest = %s, want 0 (disabled)", cfg.SlowRequest)
		}
	})
}

func TestLoadRejectsBadValues(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("APP_ENV", "development")

	for _, tt := range []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"bad duration", "WRITE_TIMEOUT", "not-a-duration", "WRITE_TIMEOUT"},
		{"bad int", "MAX_HEADER_BYTES", "many", "MAX_HEADER_BYTES"},
		{"bad bool", "ENABLE_DEBUG_ENDPOINT", "maybe", "ENABLE_DEBUG_ENDPOINT"},
		{"bad log level", "LOG_LEVEL", "garbage", "LOG_LEVEL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.val)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q = nil error, want error", tt.key, tt.val)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want string
	}{
		{"debug", "DEBUG"},
		{"DEBUG", "DEBUG"},
		{"warn", "WARN"},
		{"warning", "WARN"},
		{"error", "ERROR"},
		{"info", "INFO"},
		{"", "INFO"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			level, err := parseLogLevel(tt.in)
			if err != nil {
				t.Fatalf("parseLogLevel(%q) error: %v", tt.in, err)
			}
			if got := level.String(); got != tt.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}

	for _, tt := range []struct{ in string }{{"garbage"}, {"verbose"}} {
		t.Run("reject_"+tt.in, func(t *testing.T) {
			if _, err := parseLogLevel(tt.in); err == nil {
				t.Errorf("parseLogLevel(%q) = nil error, want error", tt.in)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := &AppConfig{
		AppEnv:            "production",
		Host:              "0.0.0.0",
		Port:              "8080",
		ServiceName:       "gogo",
		ServiceVersion:    "1.0.0",
		LogFormat:         "json",
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		RequestTimeout:    time.Second,
		ShutdownTimeout:   time.Second,
		SlowRequest:       time.Second,
		MaxHeaderBytes:    1 << 20,
		MaxBodyBytes:      1 << 20,
		DBMaxConns:        10,
		DBPingTimeout:     2 * time.Second,

		ProductAddress:              "product:9092",
		ProductListingAddress:       "product-listing:9093",
		ProductSearchAddress:        "product-search:9094",
		CategoryAddress:             "category:9090",
		ProductPlatformServiceToken: "test-service-token-123",

		Media: MediaConfig{
			MaxFileSize:      10 << 20,
			AllowedMimeTypes: []string{"image/jpeg", "image/png"},
			StoragePath:      "./storage/media",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate(nil) = %v, want no error", err)
	}

	table := []struct {
		name   string
		mutate func(*AppConfig)
		want   string
	}{
		{"bad port", func(c *AppConfig) { c.Port = "99999" }, "PORT"},
		{"non-numeric port", func(c *AppConfig) { c.Port = "http" }, "PORT"},
		{"zero request timeout", func(c *AppConfig) { c.RequestTimeout = 0 }, "REQUEST_TIMEOUT"},
		{"negative shutdown timeout", func(c *AppConfig) { c.ShutdownTimeout = -time.Second }, "SHUTDOWN_TIMEOUT"},
		{"zero read-header timeout", func(c *AppConfig) { c.ReadHeaderTimeout = 0 }, "READ_HEADER_TIMEOUT"},
		{"negative slow request", func(c *AppConfig) { c.SlowRequest = -1 }, "SLOW_REQUEST_MS"},
		{"empty service name", func(c *AppConfig) { c.ServiceName = " " }, "SERVICE_NAME"},
		{"bad app env", func(c *AppConfig) { c.AppEnv = "meta" }, "APP_ENV"},
		{"bad log format", func(c *AppConfig) { c.LogFormat = "xml" }, "LOG_FORMAT"},
		{"bad trusted proxy", func(c *AppConfig) { c.TrustedProxies = []string{"10.0.0.0/33"} }, "TRUSTED_PROXIES"},
		{"zero db max conns", func(c *AppConfig) { c.DBMaxConns = 0 }, "DB_MAX_CONNS"},
		{"db min above max", func(c *AppConfig) { c.DBMinConns = 11 }, "DB_MIN_CONNS"},
		{"zero db ping timeout", func(c *AppConfig) { c.DBPingTimeout = 0 }, "DB_PING_TIMEOUT"},

		// Every product-platform address is required. The gateway owns no part of the
		// product domain, so a missing address is not a "run it in-process instead"
		// signal — there is nothing to run in-process — and booting anyway would serve a
		// permanently empty storefront that looks like a real answer.
		{"missing product address", func(c *AppConfig) { c.ProductAddress = "" }, "PRODUCT_ADDRESS"},
		{"missing product listing address", func(c *AppConfig) { c.ProductListingAddress = "" }, "PRODUCT_LISTING_ADDRESS"},
		{"missing product search address", func(c *AppConfig) { c.ProductSearchAddress = "" }, "PRODUCT_SEARCH_ADDRESS"},
		{"missing category address", func(c *AppConfig) { c.CategoryAddress = "" }, "CATEGORY_ADDRESS"},
		{"missing product platform token", func(c *AppConfig) { c.ProductPlatformServiceToken = "" }, "PRODUCT_PLATFORM_SERVICE_TOKEN"},
		{"blank product address", func(c *AppConfig) { c.ProductAddress = "   " }, "PRODUCT_ADDRESS"},
		// A bare hostname with no port is the common typo; it would otherwise fail as an
		// opaque dial error at first request rather than at startup.
		{"product address without a port", func(c *AppConfig) { c.ProductAddress = "product" }, "PRODUCT_ADDRESS"},
		{"category address without a port", func(c *AppConfig) { c.CategoryAddress = "category" }, "CATEGORY_ADDRESS"},

		{"kafka broker without a port", func(c *AppConfig) { c.KafkaBrokers = []string{"kafka"} }, "KAFKA_BROKERS"},
	}
	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			cfg := *valid
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadModuleConfigs(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("MEDIA_MAX_FILE_SIZE", "20971520") // 20 MB
	t.Setenv("MEDIA_STORAGE_PATH", "/var/storage")
	t.Setenv("MEDIA_ALLOWED_MIME_TYPES", "image/jpeg,image/png,image/webp")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Media.MaxFileSize != 20971520 {
		t.Errorf("Media.MaxFileSize = %d, want 20971520", cfg.Media.MaxFileSize)
	}
	if cfg.Media.StoragePath != "/var/storage" {
		t.Errorf("Media.StoragePath = %q, want /var/storage", cfg.Media.StoragePath)
	}
	if len(cfg.Media.AllowedMimeTypes) != 3 {
		t.Errorf("len(Media.AllowedMimeTypes) = %d, want 3", len(cfg.Media.AllowedMimeTypes))
	}
}

func TestValidateModuleConfigs(t *testing.T) {
	t.Parallel()

	valid := &AppConfig{
		AppEnv:            "production",
		Host:              "0.0.0.0",
		Port:              "8080",
		ServiceName:       "gogo",
		ServiceVersion:    "1.0.0",
		LogFormat:         "json",
		ReadTimeout:       time.Second,
		ReadHeaderTimeout: time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		RequestTimeout:    time.Second,
		ShutdownTimeout:   time.Second,
		SlowRequest:       time.Second,
		MaxHeaderBytes:    1 << 20,
		MaxBodyBytes:      1 << 20,
		DBMaxConns:        10,
		DBPingTimeout:     2 * time.Second,

		ProductAddress:              "product:9092",
		ProductListingAddress:       "product-listing:9093",
		ProductSearchAddress:        "product-search:9094",
		CategoryAddress:             "category:9090",
		ProductPlatformServiceToken: "test-service-token-123",

		Media: MediaConfig{
			MaxFileSize:      10 << 20,
			AllowedMimeTypes: []string{"image/jpeg"},
			StoragePath:      "./storage",
		},
	}

	table := []struct {
		name   string
		mutate func(*AppConfig)
		want   string
	}{
		{"zero media max file size", func(c *AppConfig) { c.Media.MaxFileSize = 0 }, "MEDIA_MAX_FILE_SIZE"},
		{"empty allowed mime types", func(c *AppConfig) { c.Media.AllowedMimeTypes = []string{} }, "MEDIA_ALLOWED_MIME_TYPES"},
		{"empty storage path", func(c *AppConfig) { c.Media.StoragePath = "" }, "MEDIA_STORAGE_PATH"},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			cfg := *valid
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error mentioning %q", tt.want)
			} else if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadFeatureFlags(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("FEATURE_FLAGS", "new-ui,beta-feature,experimental")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.FeatureFlags) != 3 {
		t.Errorf("len(FeatureFlags) = %d, want 3", len(cfg.FeatureFlags))
	}
	if !cfg.FeatureFlags["new-ui"] {
		t.Error("expected new-ui to be enabled")
	}
	if !cfg.FeatureFlags["beta-feature"] {
		t.Error("expected beta-feature to be enabled")
	}
	if !cfg.FeatureFlags["experimental"] {
		t.Error("expected experimental to be enabled")
	}
	if cfg.FeatureFlags["disabled"] {
		t.Error("expected disabled to be false")
	}
}

func TestLoadFeatureFlagsEmpty(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("FEATURE_FLAGS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.FeatureFlags) != 0 {
		t.Errorf("len(FeatureFlags) = %d, want 0", len(cfg.FeatureFlags))
	}
}

// The change feed is optional: with no brokers the gateway runs on its cache TTLs alone,
// so a development install needs no Kafka. That is the one thing that stays optional.
func TestLoadChangeFeedDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.KafkaBrokers) != 0 {
		t.Errorf("KafkaBrokers = %v, want empty (no change feed; caches rely on their TTL)", cfg.KafkaBrokers)
	}
}

func TestLoadChangeFeedFromEnvironment(t *testing.T) {
	// NOT parallel: t.Setenv mutates the process environment.
	t.Setenv("KAFKA_BROKERS", "kafka-1:9092, kafka-2:9092")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.KafkaBrokers) != 2 || cfg.KafkaBrokers[0] != "kafka-1:9092" || cfg.KafkaBrokers[1] != "kafka-2:9092" {
		t.Errorf("KafkaBrokers = %v, want [kafka-1:9092 kafka-2:9092]", cfg.KafkaBrokers)
	}
}

// Every product-platform address has no default, so an unconfigured checkout boots to a
// configuration that Validate rejects — the failure is at startup, naming the variable,
// rather than an empty storefront that looks like a real answer.
func TestProductPlatformAddressesHaveNoDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	for name, address := range map[string]string{
		"ProductAddress":        cfg.ProductAddress,
		"ProductListingAddress": cfg.ProductListingAddress,
		"ProductSearchAddress":  cfg.ProductSearchAddress,
		"CategoryAddress":       cfg.CategoryAddress,
	} {
		if address != "" {
			t.Errorf("%s defaults to %q, want empty so validation rejects an unconfigured gateway", name, address)
		}
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate = nil for a config with no product-platform addresses, want an error")
	}
}
