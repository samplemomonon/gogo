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

	// KafkaBrokers is the seed broker list for the event stream, host:port
	// separated by commas.
	KafkaBrokers []string
	// KafkaClientID identifies this service in broker logs and metrics.
	KafkaClientID string
	// KafkaConsumerGroup is the group every replica joins, so partitions are
	// divided between them rather than each replica consuming all of them.
	//
	// It must not be the listing service's group. Two services in one group would
	// split the partitions between them, and each would then hold half the events
	// and be missing the other half of the catalogue. Separate groups are what
	// let both read the whole topic.
	KafkaConsumerGroup string
	// KafkaTopic is the topic of product events to consume.
	KafkaTopic string
	// KafkaDeadLetterTopic receives records this service cannot apply. Empty
	// disables dead-lettering.
	KafkaDeadLetterTopic string
	// KafkaMaxPollRecords bounds one fetch, and so bounds how long a rebalance is
	// blocked while a batch is applied.
	KafkaMaxPollRecords int
	// KafkaPollTimeout bounds how long a poll waits before returning empty, so a
	// cancellation is noticed promptly.
	KafkaPollTimeout time.Duration
	// KafkaRetryBackoff is how long to pause after a transient failure.
	KafkaRetryBackoff time.Duration
	// ConsumerEnabled turns the Kafka consumer off. It exists so the HTTP API can
	// be run and probed on its own, for instance while rebuilding the index.
	ConsumerEnabled bool

	// ProcessedEventRetention is how long a processed-event id is kept. Redeliveries
	// arrive within seconds, so a day is far longer than the broker will replay.
	ProcessedEventRetention time.Duration
	// PruneInterval is how often the prune runs.
	PruneInterval time.Duration

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
		DBUser:     getEnv("DB_USER", "search_user"),
		DBPassword: getEnv("DB_PASSWORD", "search_pass"),
		DBName:     getEnv("DB_NAME", "search_db"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		KafkaBrokers:         parseBrokers(getEnv("KAFKA_BROKERS", "localhost:9092")),
		KafkaClientID:        getEnv("KAFKA_CLIENT_ID", "product-search-service"),
		KafkaConsumerGroup:   getEnv("KAFKA_CONSUMER_GROUP", "product-search-service"),
		KafkaTopic:           getEnv("KAFKA_TOPIC", "product.events.v1"),
		KafkaDeadLetterTopic: getEnv("KAFKA_DEAD_LETTER_TOPIC", "product.events.v1.dlq"),

		InboundServiceTokens: parseBrokers(getEnv("INBOUND_SERVICE_TOKENS", "")),
		AppPort:              getEnv("APP_PORT", "8084"),
		GRPCPort:             getEnv("GRPC_PORT", "9094"),
		LogLevel:             getEnv("LOG_LEVEL", "info"),
		LogFormat:            strings.ToLower(getEnv("LOG_FORMAT", "json")),
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
	if cfg.KafkaMaxPollRecords, err = getEnvInt("KAFKA_MAX_POLL_RECORDS", 200); err != nil {
		return nil, err
	}
	if cfg.DBConnMaxLifetime, err = getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.KafkaPollTimeout, err = getEnvDuration("KAFKA_POLL_TIMEOUT", time.Second); err != nil {
		return nil, err
	}
	if cfg.KafkaRetryBackoff, err = getEnvDuration("KAFKA_RETRY_BACKOFF", time.Second); err != nil {
		return nil, err
	}
	if cfg.ProcessedEventRetention, err = getEnvDuration("PROCESSED_EVENT_RETENTION", 24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.PruneInterval, err = getEnvDuration("PRUNE_INTERVAL", time.Hour); err != nil {
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

	if cfg.ConsumerEnabled, err = getEnvBool("CONSUMER_ENABLED", true); err != nil {
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
	// mean "every caller is refused", not "every caller is welcome".
	if len(c.InboundServiceTokens) == 0 {
		return fmt.Errorf("config: INBOUND_SERVICE_TOKENS must list at least one token; " +
			"an absent value refuses every caller rather than admitting every one")
	}
	seenTokens := make(map[string]bool, len(c.InboundServiceTokens))
	for i, token := range c.InboundServiceTokens {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("config: INBOUND_SERVICE_TOKENS[%d] is empty", i)
		}
		if len(token) < 16 {
			return fmt.Errorf("config: INBOUND_SERVICE_TOKENS[%d] is %d characters; use at least 16",
				i, len(token))
		}
		if seenTokens[token] {
			return fmt.Errorf("config: INBOUND_SERVICE_TOKENS[%d] duplicates an earlier token", i)
		}
		seenTokens[token] = true
	}

	if len(c.KafkaBrokers) == 0 {
		return fmt.Errorf("config: KAFKA_BROKERS must list at least one host:port broker")
	}
	for _, broker := range c.KafkaBrokers {
		if _, _, err := net.SplitHostPort(broker); err != nil {
			return fmt.Errorf("config: KAFKA_BROKERS entry must be host:port, got %q", broker)
		}
	}
	if c.KafkaConsumerGroup == "" {
		return fmt.Errorf("config: KAFKA_CONSUMER_GROUP must not be empty")
	}
	if c.KafkaTopic == "" {
		return fmt.Errorf("config: KAFKA_TOPIC must not be empty")
	}
	if c.ConsumerEnabled && c.KafkaDeadLetterTopic == "" {
		// Without a dead-letter topic an unapplicable record would have to be
		// dropped or retried forever, and both are worse than a startup error.
		return fmt.Errorf(
			"config: KAFKA_DEAD_LETTER_TOPIC must be set when the consumer is enabled, " +
				"or set CONSUMER_ENABLED=false to run the search API without consuming",
		)
	}
	if c.KafkaDeadLetterTopic == c.KafkaTopic {
		return fmt.Errorf(
			"config: KAFKA_DEAD_LETTER_TOPIC must differ from KAFKA_TOPIC, both are %q",
			c.KafkaTopic,
		)
	}
	if c.KafkaMaxPollRecords < 1 {
		return fmt.Errorf(
			"config: KAFKA_MAX_POLL_RECORDS must be at least 1, got %d", c.KafkaMaxPollRecords,
		)
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
	if c.ProcessedEventRetention < time.Minute {
		// A retention window shorter than a minute would prune ids the broker
		// could still legitimately redeliver, defeating deduplication.
		return fmt.Errorf(
			"config: PROCESSED_EVENT_RETENTION must be at least 1m, got %s", c.ProcessedEventRetention,
		)
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
	return fmt.Sprintf("kafka=%s", strings.Join(c.KafkaBrokers, ","))
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

// getEnvBool reads a boolean, accepting the spellings an operator is likely to
// write in an environment variable.
func getEnvBool(key string, defaultValue bool) (bool, error) {
	raw := strings.ToLower(getEnv(key, ""))
	if raw == "" {
		return defaultValue, nil
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s must be a boolean, got %q", key, raw)
	}
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
