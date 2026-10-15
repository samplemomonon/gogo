// Package config loads and validates application configuration from the
// environment. Every setting has a development-friendly default so the service
// can start with no configuration at all, but invalid values are rejected early
// rather than failing at first use.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the application.
type Config struct {
	// Database connection settings.
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	// DBMaxOpenConns bounds the connection pool.
	DBMaxOpenConns int
	// DBMaxIdleConns bounds the idle portion of the connection pool.
	DBMaxIdleConns int
	// DBConnMaxLifetime recycles connections that have been open too long.
	DBConnMaxLifetime time.Duration
	// DBConnectTimeout bounds the initial connection and ping.
	DBConnectTimeout time.Duration

	// MinIOEndpoint is host:port of the object store, without a scheme.
	MinIOEndpoint string
	// MinIOAccessKey and MinIOSecretKey authenticate against the object store.
	MinIOAccessKey string
	MinIOSecretKey string
	// MinIOBucket is the bucket that holds every object.
	MinIOBucket string
	// MinIORegion is sent with bucket lookups; MinIO ignores it for single
	// region deployments but S3-compatible stores require it to be set.
	MinIORegion string
	// MinIOUseSSL selects https instead of http.
	MinIOUseSSL bool
	// MinIOPresignUploadTTL is how long a generated upload URL stays valid.
	MinIOPresignUploadTTL time.Duration
	// MinIOPresignDownloadTTL is the default lifetime of a download URL.
	MinIOPresignDownloadTTL time.Duration
	// MinIOEnsureBucket creates the bucket at boot if it is missing, so a fresh
	// volume needs no manual step.
	MinIOEnsureBucket bool

	// AppPort is the TCP port the HTTP server listens on.
	AppPort string

	// InboundServiceTokens are the credentials this service accepts from callers.
	// Empty does not mean "no credential required" — Validate refuses to start.
	InboundServiceTokens []string
	// GRPCPort is the TCP port the gRPC server listens on. The two listeners are
	// separate so the internal API is not exposed on the public port.
	GRPCPort string
	// GRPCMaxMessageSize caps a single inbound gRPC message in bytes.
	GRPCMaxMessageSize int

	// ReadHeaderTimeout guards against slow-header attacks.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading a whole request.
	ReadTimeout time.Duration
	// WriteTimeout bounds writing a response.
	WriteTimeout time.Duration
	// IdleTimeout bounds keep-alive connections between requests.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration

	// LogLevel is the minimum level emitted by the structured logger.
	LogLevel string
	// LogFormat selects between "json" and "text" log output.
	LogFormat string
}

// Load reads configuration from the environment, applying defaults and
// validating the result. It returns an error if any value is invalid.
func Load() (*Config, error) {
	cfg := &Config{
		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "media_user"),
		DBPassword: getEnv("DB_PASSWORD", "media_pass"),
		DBName:     getEnv("DB_NAME", "media_db"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		MinIOEndpoint:        getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey:       getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecretKey:       getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinIOBucket:          getEnv("MINIO_BUCKET", "media"),
		MinIORegion:          getEnv("MINIO_REGION", "us-east-1"),
		InboundServiceTokens: parseBrokers(getEnv("INBOUND_SERVICE_TOKENS", "")),
		AppPort:              getEnv("APP_PORT", "8081"),
		GRPCPort:             getEnv("GRPC_PORT", "9091"),
		LogLevel:             getEnv("LOG_LEVEL", "info"),
		LogFormat:            strings.ToLower(getEnv("LOG_FORMAT", "json")),
		MinIOEnsureBucket:    true,
	}

	var err error
	if cfg.DBMaxOpenConns, err = getEnvInt("DB_MAX_OPEN_CONNS", 25); err != nil {
		return nil, err
	}
	if cfg.DBMaxIdleConns, err = getEnvInt("DB_MAX_IDLE_CONNS", 10); err != nil {
		return nil, err
	}
	if cfg.GRPCMaxMessageSize, err = getEnvInt("GRPC_MAX_MESSAGE_SIZE", 16<<20); err != nil {
		return nil, err
	}
	if cfg.DBConnMaxLifetime, err = getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.MinIOPresignUploadTTL, err = getEnvDuration("MINIO_PRESIGN_UPLOAD_TTL", 15*time.Minute); err != nil {
		return nil, err
	}
	if cfg.MinIOPresignDownloadTTL, err = getEnvDuration("MINIO_PRESIGN_DOWNLOAD_TTL", 1*time.Hour); err != nil {
		return nil, err
	}
	if cfg.ReadHeaderTimeout, err = getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if cfg.ReadTimeout, err = getEnvDuration("HTTP_READ_TIMEOUT", 15*time.Second); err != nil {
		return nil, err
	}
	if cfg.WriteTimeout, err = getEnvDuration("HTTP_WRITE_TIMEOUT", 15*time.Second); err != nil {
		return nil, err
	}
	if cfg.IdleTimeout, err = getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.ShutdownTimeout, err = getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second); err != nil {
		return nil, err
	}

	// A bool that fails to parse must not silently become true, so an invalid
	// value is an error rather than the default.
	if cfg.MinIOUseSSL, err = getEnvBool("MINIO_USE_SSL", false); err != nil {
		return nil, err
	}
	if cfg.MinIOEnsureBucket, err = getEnvBool("MINIO_ENSURE_BUCKET", true); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for internally inconsistent values.
func (c *Config) Validate() error {
	// Refusing to start without a credential is the point: an absent variable must
	// mean "every caller is refused", not "every caller is welcome". This service
	// writes its own table over REST, so an open caller can change it.
	if len(c.InboundServiceTokens) == 0 {
		return fmt.Errorf("config: INBOUND_SERVICE_TOKENS must list at least one token; " +
			"an absent value refuses every caller rather than admitting every one")
	}
	for i, token := range c.InboundServiceTokens {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("config: INBOUND_SERVICE_TOKENS[%d] is empty", i)
		}
		if len(token) < 16 {
			return fmt.Errorf("config: INBOUND_SERVICE_TOKENS[%d] is %d characters; use at least 16",
				i, len(token))
		}
	}

	if _, err := parsePort("APP_PORT", c.AppPort); err != nil {
		return err
	}
	if _, err := parsePort("GRPC_PORT", c.GRPCPort); err != nil {
		return err
	}
	if _, err := parsePort("DB_PORT", c.DBPort); err != nil {
		return err
	}
	if c.DBUser == "" {
		return fmt.Errorf("config: DB_USER must not be empty")
	}
	if c.DBName == "" {
		return fmt.Errorf("config: DB_NAME must not be empty")
	}
	if c.DBMaxOpenConns < 1 {
		return fmt.Errorf("config: DB_MAX_OPEN_CONNS must be at least 1, got %d", c.DBMaxOpenConns)
	}
	if c.DBMaxIdleConns < 0 {
		return fmt.Errorf("config: DB_MAX_IDLE_CONNS must not be negative, got %d", c.DBMaxIdleConns)
	}
	if c.DBMaxIdleConns > c.DBMaxOpenConns {
		return fmt.Errorf(
			"config: DB_MAX_IDLE_CONNS (%d) must not exceed DB_MAX_OPEN_CONNS (%d)",
			c.DBMaxIdleConns, c.DBMaxOpenConns,
		)
	}
	if c.GRPCMaxMessageSize < 1024 {
		return fmt.Errorf("config: GRPC_MAX_MESSAGE_SIZE must be at least 1024, got %d", c.GRPCMaxMessageSize)
	}

	validSSLMode := map[string]bool{
		"disable":     true,
		"allow":       true,
		"prefer":      true,
		"require":     true,
		"verify-ca":   true,
		"verify-full": true,
	}
	if !validSSLMode[c.DBSSLMode] {
		return fmt.Errorf("config: DB_SSLMODE must be one of disable|allow|prefer|require|verify-ca|verify-full, got %q", c.DBSSLMode)
	}

	validLogFormats := map[string]bool{"json": true, "text": true}
	if !validLogFormats[c.LogFormat] {
		return fmt.Errorf("config: LOG_FORMAT must be json or text, got %q", c.LogFormat)
	}

	if c.MinIOEndpoint == "" {
		return fmt.Errorf("config: MINIO_ENDPOINT must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.MinIOEndpoint); err != nil {
		return fmt.Errorf("config: MINIO_ENDPOINT must be host:port, got %q", c.MinIOEndpoint)
	}
	if c.MinIOAccessKey == "" || c.MinIOSecretKey == "" {
		return fmt.Errorf("config: MINIO_ACCESS_KEY and MINIO_SECRET_KEY must not be empty")
	}
	if err := validateBucketName(c.MinIOBucket); err != nil {
		return err
	}
	if c.MinIORegion == "" {
		return fmt.Errorf("config: MINIO_REGION must not be empty")
	}
	// The SDK caps presigned URLs at seven days; asking for more produces a URL
	// that silently expires early, so reject it at start instead.
	const maxPresignTTL = 7 * 24 * time.Hour
	if c.MinIOPresignUploadTTL > maxPresignTTL {
		return fmt.Errorf("config: MINIO_PRESIGN_UPLOAD_TTL must not exceed 168h, got %s", c.MinIOPresignUploadTTL)
	}
	if c.MinIOPresignDownloadTTL > maxPresignTTL {
		return fmt.Errorf("config: MINIO_PRESIGN_DOWNLOAD_TTL must not exceed 168h, got %s", c.MinIOPresignDownloadTTL)
	}
	return nil
}

// Addr returns the address the HTTP server should listen on.
func (c *Config) Addr() string {
	return net.JoinHostPort("", c.AppPort)
}

// GRPCAddr returns the address the gRPC server should listen on.
func (c *Config) GRPCAddr() string {
	return net.JoinHostPort("", c.GRPCPort)
}

// DSN returns the PostgreSQL connection string. The password is included
// because lib/pq has no separate authentication mechanism; never log this value.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=%d",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
		int(c.DBConnectTimeout.Seconds()),
	)
}

// RedactedDSN returns the connection string with the password masked, safe for
// logging and diagnostics.
func (c *Config) RedactedDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=**** dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBName, c.DBSSLMode,
	)
}

// RedactedMinIO returns the object store endpoint with the secret masked.
func (c *Config) RedactedMinIO() string {
	scheme := "http"
	if c.MinIOUseSSL {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/%s (access key %s)",
		scheme, c.MinIOEndpoint, c.MinIOBucket, c.MinIOAccessKey,
	)
}

// validateBucketName applies the S3 naming rules that matter here. Catching them
// at start avoids a confusing failure from the object store later.
func validateBucketName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("config: MINIO_BUCKET must not be empty")
	case len(name) < 3 || len(name) > 63:
		return fmt.Errorf("config: MINIO_BUCKET must be 3 to 63 characters, got %d", len(name))
	}

	for i, r := range name {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		isHyphen := r == '-'
		if isLower || isDigit || isHyphen {
			continue
		}
		return fmt.Errorf(
			"config: MINIO_BUCKET may only contain lowercase letters, digits and hyphens, "+
				"found %q at position %d", r, i,
		)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("config: MINIO_BUCKET must not start or end with a hyphen, got %q", name)
	}
	// A doubled hyphen is reserved by the S3 naming rules.
	if strings.Contains(name, "--") {
		return fmt.Errorf("config: MINIO_BUCKET must not contain consecutive hyphens, got %q", name)
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) (int, error) {
	raw := getEnv(key, "")
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, raw)
	}
	return value, nil
}

func getEnvBool(key string, defaultValue bool) (bool, error) {
	raw := strings.ToLower(getEnv(key, ""))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean like true or false, got %q", key, raw)
	}
	return value, nil
}

func getEnvDuration(key string, defaultValue time.Duration) (time.Duration, error) {
	raw := getEnv(key, "")
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration such as 30s or 5m, got %q", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("config: %s must be positive, got %s", key, value)
	}
	return value, nil
}

func parsePort(name, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a number, got %q", name, value)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("config: %s must be between 1 and 65535, got %d", name, port)
	}
	return port, nil
}

// parseBrokers splits a comma-separated environment value, dropping empties.
//
// It is the same shape as parseBrokers in the other services: a list in the
// environment is a string, and an operator who leaves a trailing comma meant the
// items before it, not an empty item.
func parseBrokers(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
