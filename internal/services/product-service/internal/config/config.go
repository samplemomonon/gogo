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

	// CategoryGRPCTarget is host:port of the category service's gRPC listener.
	CategoryGRPCTarget string
	// MediaGRPCTarget is host:port of the media service's gRPC listener.
	MediaGRPCTarget string
	// DependencyTimeout bounds a single outbound gRPC call, so an unresponsive
	// dependency cannot hold a request open indefinitely.
	DependencyTimeout time.Duration

	// DefaultCurrency is the ISO 4217 code applied when a request omits one.
	DefaultCurrency string

	// AppPort is the TCP port the HTTP server listens on.
	AppPort string
	// GRPCPort is the TCP port the gRPC server listens on. The two listeners are
	// separate so the internal API is not exposed on the public port.
	GRPCPort string
	// GRPCMaxMessageSize caps a single inbound gRPC message in bytes.
	GRPCMaxMessageSize int

	// KafkaBrokers is the seed broker list for the event stream, host:port
	// separated by commas. One reachable broker is enough; the client discovers
	// the rest of the cluster from it.
	KafkaBrokers []string
	// KafkaPublishTimeout bounds one produce call, so a broker that accepts a
	// connection but never acknowledges cannot pin the relay forever.
	KafkaPublishTimeout time.Duration
	// KafkaClientID identifies this producer in broker logs and metrics.
	KafkaClientID string

	// OutboxBatchSize is how many events one relay claim takes.
	OutboxBatchSize int
	// OutboxPollInterval is the fallback sweep. The outbox trigger also wakes
	// the relay over LISTEN/NOTIFY, so this only bounds the latency of an event
	// whose notification was missed.
	OutboxPollInterval time.Duration
	// OutboxLease is how long a claimed batch stays reserved to one relay.
	OutboxLease time.Duration
	// OutboxMaxAttempts is how many times an event is tried before it is marked
	// failed and left for an operator.
	OutboxMaxAttempts int

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
		DBUser:     getEnv("DB_USER", "product_user"),
		DBPassword: getEnv("DB_PASSWORD", "product_pass"),
		DBName:     getEnv("DB_NAME", "product_db"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		CategoryGRPCTarget: getEnv("CATEGORY_GRPC_TARGET", "localhost:9090"),
		MediaGRPCTarget:    getEnv("MEDIA_GRPC_TARGET", "localhost:9091"),
		DefaultCurrency:    getEnv("DEFAULT_CURRENCY", "IDR"),
		AppPort:            getEnv("APP_PORT", "8082"),
		GRPCPort:           getEnv("GRPC_PORT", "9092"),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		LogFormat:          strings.ToLower(getEnv("LOG_FORMAT", "json")),

		KafkaBrokers:  parseBrokers(getEnv("KAFKA_BROKERS", "localhost:9092")),
		KafkaClientID: getEnv("KAFKA_CLIENT_ID", "product-service"),
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
	if cfg.OutboxBatchSize, err = getEnvInt("OUTBOX_BATCH_SIZE", 100); err != nil {
		return nil, err
	}
	if cfg.OutboxMaxAttempts, err = getEnvInt("OUTBOX_MAX_ATTEMPTS", 10); err != nil {
		return nil, err
	}
	if cfg.DBConnMaxLifetime, err = getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.KafkaPublishTimeout, err = getEnvDuration("KAFKA_PUBLISH_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.OutboxPollInterval, err = getEnvDuration("OUTBOX_POLL_INTERVAL", 5*time.Second); err != nil {
		return nil, err
	}
	if cfg.OutboxLease, err = getEnvDuration("OUTBOX_LEASE", 30*time.Second); err != nil {
		return nil, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.DependencyTimeout, err = getEnvDuration("DEPENDENCY_TIMEOUT", 3*time.Second); err != nil {
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

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for internally inconsistent values.
func (c *Config) Validate() error {
	if len(c.KafkaBrokers) == 0 {
		return fmt.Errorf("config: KAFKA_BROKERS must list at least one host:port broker")
	}
	for _, broker := range c.KafkaBrokers {
		if _, _, err := net.SplitHostPort(broker); err != nil {
			return fmt.Errorf("config: KAFKA_BROKERS entry must be host:port, got %q", broker)
		}
	}
	if c.OutboxBatchSize < 1 {
		return fmt.Errorf("config: OUTBOX_BATCH_SIZE must be at least 1, got %d", c.OutboxBatchSize)
	}
	if c.OutboxMaxAttempts < 1 {
		return fmt.Errorf("config: OUTBOX_MAX_ATTEMPTS must be at least 1, got %d", c.OutboxMaxAttempts)
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

	// A gRPC target is host:port, so a missing port is caught here rather than
	// surfacing as a confusing dial error on the first call.
	if _, _, err := net.SplitHostPort(c.CategoryGRPCTarget); err != nil {
		return fmt.Errorf("config: CATEGORY_GRPC_TARGET must be host:port, got %q", c.CategoryGRPCTarget)
	}
	if _, _, err := net.SplitHostPort(c.MediaGRPCTarget); err != nil {
		return fmt.Errorf("config: MEDIA_GRPC_TARGET must be host:port, got %q", c.MediaGRPCTarget)
	}
	if err := validateCurrencyCode("DEFAULT_CURRENCY", c.DefaultCurrency); err != nil {
		return err
	}
	return nil
}

// validateCurrencyCode applies the ISO 4217 shape used by the currency CHECK
// constraint, so a bad default is caught at start rather than on the first
// insert.
func validateCurrencyCode(name, value string) error {
	if len(value) != 3 {
		return fmt.Errorf("config: %s must be a 3 letter currency code, got %q", name, value)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return fmt.Errorf("config: %s must be uppercase letters, got %q", name, value)
		}
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

// Dependencies returns the outbound endpoints this service talks to, for logging.
// None of them carry credentials, so they are safe to print.
func (c *Config) Dependencies() string {
	return fmt.Sprintf("category=%s media=%s kafka=%s",
		c.CategoryGRPCTarget, c.MediaGRPCTarget, strings.Join(c.KafkaBrokers, ","),
	)
}

// parseBrokers splits a comma separated broker list, discarding empty entries so
// a trailing comma or a blank value does not become a broker named "".
func parseBrokers(raw string) []string {
	parts := strings.Split(raw, ",")
	brokers := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			brokers = append(brokers, trimmed)
		}
	}
	return brokers
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
