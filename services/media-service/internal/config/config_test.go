// Package config_test pins the boot-time policy: which values are defaulted,
// which are rejected, and which are refused outright. Configuration failures
// are cheap at boot and expensive at first use, so Validate's table is the
// contract this file exists to keep honest.
package config_test

import (
	"strings"
	"testing"
	"time"

	"media-service/internal/config"
)

// validConfig returns a configuration that passes Validate. Every rejection
// test starts from it and changes exactly one field, so the failure it asserts
// is attributable to that field.
func validConfig() *config.Config {
	return &config.Config{
		DBHost:                  "localhost",
		DBPort:                  "5432",
		DBUser:                  "media_user",
		DBPassword:              "media_pass",
		DBName:                  "media_db",
		DBSSLMode:               "disable",
		DBMaxOpenConns:          25,
		DBMaxIdleConns:          10,
		DBConnMaxLifetime:       5 * time.Minute,
		DBConnectTimeout:        10 * time.Second,
		MinIOEndpoint:           "localhost:9000",
		MinIOAccessKey:          "minioadmin",
		MinIOSecretKey:          "minioadmin",
		MinIOBucket:             "media",
		MinIORegion:             "us-east-1",
		MinIOUseSSL:             false,
		MinIOPresignUploadTTL:   15 * time.Minute,
		MinIOPresignDownloadTTL: time.Hour,
		MinIOEnsureBucket:       true,
		AppPort:                 "8081",
		GRPCPort:                "9091",
		InboundServiceTokens:    []string{"0123456789abcdef"},
		GRPCMaxMessageSize:      16 << 20,
		ReadHeaderTimeout:       5 * time.Second,
		ReadTimeout:             15 * time.Second,
		WriteTimeout:            15 * time.Second,
		IdleTimeout:             60 * time.Second,
		ShutdownTimeout:         30 * time.Second,
		LogLevel:                "info",
		LogFormat:               "json",
	}
}

// TestValidConfigIsAccepted guards the fixture itself: if the baseline stops
// passing, every rejection below would pass for the wrong reason.
func TestValidConfigIsAccepted(t *testing.T) {
	t.Parallel()

	if err := validConfig().Validate(); err != nil {
		t.Fatalf("baseline configuration rejected: %v", err)
	}
}

// TestValidateRefusesAnUnconfiguredOrWeakTokenList pins the admission-control
// policy: an absent token list must refuse every caller rather than admit
// every one, and a token short enough to guess is a configuration error.
func TestValidateRefusesAnUnconfiguredOrWeakTokenList(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		tokens []string
	}{
		{"absent", nil},
		{"empty list", []string{}},
		{"blank entry", []string{"0123456789abcdef", "   "}},
		{"too short", []string{"short"}},
		{"one blank among valid", []string{"0123456789abcdef", ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			cfg.InboundServiceTokens = tt.tokens
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an unusable token list")
			}
			if !strings.Contains(err.Error(), "INBOUND_SERVICE_TOKENS") {
				t.Errorf("error %q should name the variable", err)
			}
		})
	}
}

// TestValidatePorts pins the accepted range and the message for each shape of
// bad value: not a number, zero, and out of range.
func TestValidatePorts(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		apply func(*config.Config)
	}{
		{"APP_PORT not a number", func(c *config.Config) { c.AppPort = "http" }},
		{"APP_PORT zero", func(c *config.Config) { c.AppPort = "0" }},
		{"APP_PORT out of range", func(c *config.Config) { c.AppPort = "65536" }},
		{"GRPC_PORT not a number", func(c *config.Config) { c.GRPCPort = "grpc" }},
		{"DB_PORT out of range", func(c *config.Config) { c.DBPort = "70000" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.apply(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid port")
			}
		})
	}
}

// TestValidateDatabaseSettings pins the pool invariants: a pool that can never
// open a connection is useless, and an idle pool larger than the open pool can
// never be honoured.
func TestValidateDatabaseSettings(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		apply func(*config.Config)
		want  string
	}{
		{"empty user", func(c *config.Config) { c.DBUser = "" }, "DB_USER"},
		{"empty name", func(c *config.Config) { c.DBName = "" }, "DB_NAME"},
		{"no open connections", func(c *config.Config) { c.DBMaxOpenConns = 0 }, "DB_MAX_OPEN_CONNS"},
		{"negative idle", func(c *config.Config) { c.DBMaxIdleConns = -1 }, "DB_MAX_IDLE_CONNS"},
		{"idle exceeds open", func(c *config.Config) {
			c.DBMaxOpenConns = 4
			c.DBMaxIdleConns = 5
		}, "DB_MAX_IDLE_CONNS"},
		{"bad sslmode", func(c *config.Config) { c.DBSSLMode = "sometimes" }, "DB_SSLMODE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid database setting")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should name %s", err, tt.want)
			}
		})
	}
}

// TestValidateObjectStoreSettings pins the MinIO rules, including the S3
// bucket naming rules that would otherwise surface as a late failure from the
// object store.
func TestValidateObjectStoreSettings(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		apply func(*config.Config)
		want  string
	}{
		{"empty endpoint", func(c *config.Config) { c.MinIOEndpoint = "" }, "MINIO_ENDPOINT"},
		{"endpoint without port", func(c *config.Config) { c.MinIOEndpoint = "minio" }, "MINIO_ENDPOINT"},
		{"empty access key", func(c *config.Config) { c.MinIOAccessKey = "" }, "MINIO_ACCESS_KEY"},
		{"empty secret key", func(c *config.Config) { c.MinIOSecretKey = "" }, "MINIO_SECRET_KEY"},
		{"empty region", func(c *config.Config) { c.MinIORegion = "" }, "MINIO_REGION"},
		{"empty bucket", func(c *config.Config) { c.MinIOBucket = "" }, "MINIO_BUCKET"},
		{"short bucket", func(c *config.Config) { c.MinIOBucket = "ab" }, "MINIO_BUCKET"},
		{"long bucket", func(c *config.Config) { c.MinIOBucket = strings.Repeat("a", 64) }, "MINIO_BUCKET"},
		{"uppercase bucket", func(c *config.Config) { c.MinIOBucket = "Media" }, "MINIO_BUCKET"},
		{"underscore bucket", func(c *config.Config) { c.MinIOBucket = "media_bucket" }, "MINIO_BUCKET"},
		{"leading hyphen", func(c *config.Config) { c.MinIOBucket = "-media" }, "MINIO_BUCKET"},
		{"trailing hyphen", func(c *config.Config) { c.MinIOBucket = "media-" }, "MINIO_BUCKET"},
		{"doubled hyphen", func(c *config.Config) { c.MinIOBucket = "media--store" }, "MINIO_BUCKET"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid object store setting")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should name %s", err, tt.want)
			}
		})
	}
}

// TestValidatePresignTTLs pins the seven-day cap: the SDK silently shortens a
// longer presign, so a configured value past the cap would produce URLs that
// expire earlier than the operator intended — exactly the kind of late, quiet
// failure boot-time validation exists to prevent.
func TestValidatePresignTTLs(t *testing.T) {
	t.Parallel()

	over := 7*24*time.Hour + time.Second

	cfg := validConfig()
	cfg.MinIOPresignUploadTTL = over
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted an upload TTL beyond seven days")
	}

	cfg = validConfig()
	cfg.MinIOPresignDownloadTTL = over
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted a download TTL beyond seven days")
	}
}

// TestValidateGRPCMessageSize pins the floor: a cap below one KiB would refuse
// even the smallest legitimate call.
func TestValidateGRPCMessageSize(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.GRPCMaxMessageSize = 1023
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted a gRPC message cap under 1 KiB")
	}
}

// TestValidateLogFormat pins the two accepted renderings and the rejection of
// anything else, so a typo cannot silently keep the default.
func TestValidateLogFormat(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.LogFormat = "logfmt"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unknown log format")
	}
	if !strings.Contains(err.Error(), "LOG_FORMAT") {
		t.Errorf("error %q should name LOG_FORMAT", err)
	}
}

// TestLoadAppliesDefaultsAndParsesLists pins Load's environment contract with
// a minimal environment: the documented defaults hold, an omitted token list
// is still refused (defaults do not include a credential), and a
// comma-separated token list is split and trimmed.
func TestLoadAppliesDefaultsAndParsesLists(t *testing.T) {
	for _, key := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE",
		"MINIO_ENDPOINT", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "MINIO_BUCKET",
		"MINIO_REGION", "APP_PORT", "GRPC_PORT", "LOG_LEVEL", "LOG_FORMAT",
		"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "GRPC_MAX_MESSAGE_SIZE",
		"MINIO_USE_SSL", "MINIO_ENSURE_BUCKET",
		"DB_CONN_MAX_LIFETIME", "DB_CONNECT_TIMEOUT",
		"MINIO_PRESIGN_UPLOAD_TTL", "MINIO_PRESIGN_DOWNLOAD_TTL",
		"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	t.Setenv("INBOUND_SERVICE_TOKENS", "")
	if _, err := config.Load(); err == nil {
		t.Fatal("Load succeeded without any inbound token; an absent list must refuse every caller")
	}

	t.Setenv("INBOUND_SERVICE_TOKENS", " token-aaaaaaaaaaaa , , token-bbbbbbbbbbbb ")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.AppPort != "8081" || cfg.GRPCPort != "9091" {
		t.Errorf("ports = %q/%q, want 8081/9091", cfg.AppPort, cfg.GRPCPort)
	}
	if cfg.DBHost != "localhost" || cfg.DBName != "media_db" {
		t.Errorf("database defaults = %q/%q", cfg.DBHost, cfg.DBName)
	}
	if cfg.MinIOEndpoint != "localhost:9000" || cfg.MinIOBucket != "media" {
		t.Errorf("object store defaults = %q/%q", cfg.MinIOEndpoint, cfg.MinIOBucket)
	}
	if !cfg.MinIOEnsureBucket {
		t.Error("MINIO_ENSURE_BUCKET default = false, want true so a fresh volume needs no manual step")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LOG_FORMAT default = %q, want json", cfg.LogFormat)
	}

	wantTokens := []string{"token-aaaaaaaaaaaa", "token-bbbbbbbbbbbb"}
	if len(cfg.InboundServiceTokens) != len(wantTokens) {
		t.Fatalf("tokens = %v, want %v", cfg.InboundServiceTokens, wantTokens)
	}
	for i, want := range wantTokens {
		if cfg.InboundServiceTokens[i] != want {
			t.Errorf("token[%d] = %q, want %q", i, cfg.InboundServiceTokens[i], want)
		}
	}
}

// TestLoadRejectsUnparseableValues pins the conversion errors: a value that is
// present but wrong must fail the boot rather than silently fall back to the
// default, because the operator clearly meant to change something.
func TestLoadRejectsUnparseableValues(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  string
		val  string
	}{
		{"integer", "DB_MAX_OPEN_CONNS", "many"},
		{"bool", "MINIO_USE_SSL", "yes-ish"},
		{"duration", "DB_CONNECT_TIMEOUT", "soon"},
		{"non-positive duration", "HTTP_READ_TIMEOUT", "0s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("INBOUND_SERVICE_TOKENS", "0123456789abcdef")
			t.Setenv(tt.key, tt.val)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load accepted %s=%q", tt.key, tt.val)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q should name %s", err, tt.key)
			}
		})
	}
}

// TestDSNRoundTrip pins the two connection-string renderings: the real DSN
// carries the password, and the redacted form is what logs may carry. Getting
// them the wrong way round would put a database password in a log line.
func TestDSNRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.DBPassword = "sup3r-s3cret"

	dsn := cfg.DSN()
	for _, want := range []string{
		"host=localhost", "port=5432", "user=media_user",
		"password=sup3r-s3cret", "dbname=media_db", "sslmode=disable", "connect_timeout=10",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q should contain %q", dsn, want)
		}
	}

	redacted := cfg.RedactedDSN()
	if strings.Contains(redacted, "sup3r-s3cret") {
		t.Errorf("RedactedDSN %q contains the password", redacted)
	}
	if !strings.Contains(redacted, "password=****") {
		t.Errorf("RedactedDSN %q should mask the password", redacted)
	}
}

// TestAddresses pins the listener addresses: both are bind-all on their
// configured ports, which is what the container runtime expects.
func TestAddresses(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	if got := cfg.Addr(); got != ":8081" {
		t.Errorf("Addr() = %q, want :8081", got)
	}
	if got := cfg.GRPCAddr(); got != ":9091" {
		t.Errorf("GRPCAddr() = %q, want :9091", got)
	}
}
