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

	// AppPort is the TCP port the HTTP server listens on.
	AppPort string

	// InboundServiceTokens are the credentials this service accepts from callers.
	// Empty does not mean "no credential required" — Validate refuses to start.
	InboundServiceTokens []string

	// GRPCPort is the TCP port the gRPC server listens on. It is separate from
	// AppPort so the internal API is not exposed on the public port.
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
		DBUser:     getEnv("DB_USER", "category_user"),
		DBPassword: getEnv("DB_PASSWORD", "category_pass"),
		DBName:     getEnv("DB_NAME", "category_db"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		InboundServiceTokens: parseBrokers(getEnv("INBOUND_SERVICE_TOKENS", "")),
		AppPort:              getEnv("APP_PORT", "8086"),
		GRPCPort:             getEnv("GRPC_PORT", "9090"),
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
	if cfg.DBConnMaxLifetime, err = getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute); err != nil {
		return nil, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", 10*time.Second); err != nil {
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
	password := c.DBPassword
	if password == "" {
		password = "****"
	} else {
		password = "****"
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, password, c.DBName, c.DBSSLMode,
	)
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
