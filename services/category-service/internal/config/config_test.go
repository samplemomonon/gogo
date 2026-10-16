package config

import (
	"strings"
	"testing"
	"time"
)

// A valid configuration needs an inbound credential: Load refuses to start
// without one, because an absent variable must refuse every caller rather than
// admit every one.
const configTestToken = "config-test-inbound-token-value"

func withToken(t *testing.T) {
	t.Helper()
	t.Setenv("INBOUND_SERVICE_TOKENS", configTestToken)
}

func TestLoadDefaults(t *testing.T) {
	// Load reads the process environment, so this test must not run in parallel.
	withToken(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 8086, not 8080: the gateway listens on 8080 and two services cannot share a
	// host port. ports.txt records the assignment.
	if cfg.AppPort != "8086" {
		t.Errorf("AppPort = %q, want 8086", cfg.AppPort)
	}
	if cfg.DBSSLMode != "disable" {
		t.Errorf("DBSSLMode = %q, want disable", cfg.DBSSLMode)
	}
	if cfg.Addr() != ":8086" {
		t.Errorf("Addr() = %q, want :8086", cfg.Addr())
	}
	if cfg.DBConnMaxLifetime != 5*time.Minute {
		t.Errorf("DBConnMaxLifetime = %v, want 5m", cfg.DBConnMaxLifetime)
	}
}

func TestDSNRedaction(t *testing.T) {
	t.Setenv("DB_PASSWORD", "super-secret")

	withToken(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if strings.Contains(cfg.RedactedDSN(), "super-secret") {
		t.Error("RedactedDSN leaks the password")
	}
	if !strings.Contains(cfg.DSN(), "super-secret") {
		t.Error("DSN should contain the password for lib/pq")
	}
	// lib/pq wants connect_timeout as whole seconds.
	if !strings.Contains(cfg.DSN(), "connect_timeout=") {
		t.Error("DSN is missing connect_timeout")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "non numeric port",
			mutate:  func(c *Config) { c.AppPort = "http" },
			wantErr: "APP_PORT must be a number",
		},
		{
			name:    "port out of range",
			mutate:  func(c *Config) { c.AppPort = "70000" },
			wantErr: "APP_PORT must be between 1 and 65535",
		},
		{
			name:    "empty user",
			mutate:  func(c *Config) { c.DBUser = "" },
			wantErr: "DB_USER must not be empty",
		},
		{
			name:    "empty database",
			mutate:  func(c *Config) { c.DBName = "" },
			wantErr: "DB_NAME must not be empty",
		},
		{
			name:    "bad ssl mode",
			mutate:  func(c *Config) { c.DBSSLMode = "maybe" },
			wantErr: "DB_SSLMODE must be one of",
		},
		{
			name:    "bad log format",
			mutate:  func(c *Config) { c.LogFormat = "yaml" },
			wantErr: "LOG_FORMAT must be json or text",
		},
		{
			name:    "idle exceeds open",
			mutate:  func(c *Config) { c.DBMaxIdleConns = c.DBMaxOpenConns + 1 },
			wantErr: "must not exceed",
		},
		{
			name:    "zero open conns",
			mutate:  func(c *Config) { c.DBMaxOpenConns = 0 },
			wantErr: "DB_MAX_OPEN_CONNS must be at least 1",
		},
		{
			name:    "grpc port out of range",
			mutate:  func(c *Config) { c.GRPCPort = "70000" },
			wantErr: "GRPC_PORT must be between 1 and 65535",
		},
		{
			name:    "grpc message size too small",
			mutate:  func(c *Config) { c.GRPCMaxMessageSize = 16 },
			wantErr: "GRPC_MAX_MESSAGE_SIZE must be at least 1024",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				// Set so the rule under test is the one being mutated. Without it
				// Validate stops at the credential check and every case fails for
				// the same wrong reason.
				InboundServiceTokens: []string{"config-test-inbound-token-value"},
				DBHost:               "localhost",
				DBPort:               "5432",
				DBUser:               "user",
				DBName:               "db",
				DBSSLMode:            "disable",
				AppPort:              "8086",
				LogLevel:             "info",
				LogFormat:            "json",
				DBMaxOpenConns:       25,
				DBMaxIdleConns:       10,
				GRPCPort:             "9090",
				// Valid, so the port and size checks pass and each case fails on
				// the field it actually targets.
				GRPCMaxMessageSize: 16 << 20,
			}
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadRejectsBadEnv(t *testing.T) {
	withToken(t)
	t.Setenv("HTTP_READ_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("want an error for a malformed duration, got nil")
	}
}

func TestLoadAcceptsValidEnv(t *testing.T) {
	withToken(t)
	t.Setenv("APP_PORT", "9090")
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("HTTP_READ_TIMEOUT", "5s")
	t.Setenv("DB_MAX_IDLE_CONNS", "4")

	withToken(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.AppPort != "9090" {
		t.Errorf("AppPort = %q, want 9090", cfg.AppPort)
	}
	if cfg.Addr() != ":9090" {
		t.Errorf("Addr() = %q, want :9090", cfg.Addr())
	}
	if cfg.DBSSLMode != "require" {
		t.Errorf("DBSSLMode = %q, want require", cfg.DBSSLMode)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
	if cfg.ReadTimeout != 5*time.Second {
		t.Errorf("ReadTimeout = %v, want 5s", cfg.ReadTimeout)
	}
	if cfg.DBMaxIdleConns != 4 {
		t.Errorf("DBMaxIdleConns = %d, want 4", cfg.DBMaxIdleConns)
	}
}
