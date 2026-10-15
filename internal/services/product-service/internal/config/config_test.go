package config

import (
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every variable this package reads, so a developer's shell does
// not decide what a test asserts. t.Setenv restores the previous value
// afterwards, which keeps the tests independent of each other.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE",
		"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME",
		"DB_CONNECT_TIMEOUT", "CATEGORY_GRPC_TARGET", "MEDIA_GRPC_TARGET",
		"DEPENDENCY_TIMEOUT", "DEFAULT_CURRENCY", "APP_PORT", "GRPC_PORT",
		"GRPC_MAX_MESSAGE_SIZE", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT",
		"HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT",
		"LOG_LEVEL", "LOG_FORMAT", "KAFKA_BROKERS", "KAFKA_CLIENT_ID",
		"KAFKA_PUBLISH_TIMEOUT", "OUTBOX_BATCH_SIZE", "OUTBOX_POLL_INTERVAL",
		"OUTBOX_LEASE", "OUTBOX_MAX_ATTEMPTS",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.AppPort != "8082" {
		t.Errorf("APP_PORT = %q, want 8082", cfg.AppPort)
	}
	if cfg.GRPCPort != "9092" {
		t.Errorf("GRPC_PORT = %q, want 9092", cfg.GRPCPort)
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "localhost:9092" {
		t.Errorf("KAFKA_BROKERS = %v, want [localhost:9092]", cfg.KafkaBrokers)
	}
	if cfg.OutboxPollInterval != 5*time.Second {
		t.Errorf("OUTBOX_POLL_INTERVAL = %s, want 5s", cfg.OutboxPollInterval)
	}
	if cfg.DefaultCurrency != "IDR" {
		t.Errorf("DEFAULT_CURRENCY = %q, want IDR", cfg.DefaultCurrency)
	}
}

// The service must start with no configuration at all, which is what lets it boot
// before the broker and the other services are up.
func TestLoadWithoutAnyEnvironment(t *testing.T) {
	clearEnv(t)

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestKafkaBrokersAreParsedAsAList(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_BROKERS", "broker-1:9092, broker-2:9092 ,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.KafkaBrokers) != 2 {
		t.Fatalf("brokers = %v, want 2 entries with the blank one dropped", cfg.KafkaBrokers)
	}
	if cfg.KafkaBrokers[0] != "broker-1:9092" || cfg.KafkaBrokers[1] != "broker-2:9092" {
		t.Errorf("brokers = %v, want the trimmed values", cfg.KafkaBrokers)
	}
}

// A broker entry without a port would surface as a confusing dial error on the
// first publish, so it is caught at startup instead.
func TestKafkaBrokerMustBeHostPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_BROKERS", "broker-1")

	_, err := Load()
	if err == nil {
		t.Fatal("err = nil, want a missing port to be rejected")
	}
	if !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Errorf("err = %q, want it to name the variable", err)
	}
}

func TestKafkaBrokersMustNotBeEmpty(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_BROKERS", " , ")

	if _, err := Load(); err == nil {
		t.Error("err = nil, want an empty broker list to be rejected")
	}
}

func TestOutboxSettingsAreConfigurable(t *testing.T) {
	clearEnv(t)
	t.Setenv("OUTBOX_BATCH_SIZE", "250")
	t.Setenv("OUTBOX_POLL_INTERVAL", "1s")
	t.Setenv("OUTBOX_LEASE", "45s")
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	t.Setenv("KAFKA_PUBLISH_TIMEOUT", "5s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OutboxBatchSize != 250 {
		t.Errorf("OUTBOX_BATCH_SIZE = %d, want 250", cfg.OutboxBatchSize)
	}
	if cfg.OutboxPollInterval != time.Second {
		t.Errorf("OUTBOX_POLL_INTERVAL = %s, want 1s", cfg.OutboxPollInterval)
	}
	if cfg.OutboxLease != 45*time.Second {
		t.Errorf("OUTBOX_LEASE = %s, want 45s", cfg.OutboxLease)
	}
	if cfg.OutboxMaxAttempts != 3 {
		t.Errorf("OUTBOX_MAX_ATTEMPTS = %d, want 3", cfg.OutboxMaxAttempts)
	}
	if cfg.KafkaPublishTimeout != 5*time.Second {
		t.Errorf("KAFKA_PUBLISH_TIMEOUT = %s, want 5s", cfg.KafkaPublishTimeout)
	}
}

func TestInvalidValuesAreRejected(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"non-numeric batch size": {"OUTBOX_BATCH_SIZE", "many"},
		"zero batch size":        {"OUTBOX_BATCH_SIZE", "0"},
		"zero attempts":          {"OUTBOX_MAX_ATTEMPTS", "0"},
		"bad duration":           {"OUTBOX_POLL_INTERVAL", "soon"},
		"zero duration":          {"OUTBOX_LEASE", "0s"},
		"non-numeric port":       {"APP_PORT", "http"},
		"port out of range":      {"APP_PORT", "70000"},
		"unknown ssl mode":       {"DB_SSLMODE", "maybe"},
		"unknown log format":     {"LOG_FORMAT", "yaml"},
		"lowercase currency":     {"DEFAULT_CURRENCY", "idr"},
		"four letter currency":   {"DEFAULT_CURRENCY", "IDRX"},
		"grpc target no port":    {"CATEGORY_GRPC_TARGET", "category-app"},
		"small message limit":    {"GRPC_MAX_MESSAGE_SIZE", "16"},
		"idle exceeds max open":  {"DB_MAX_IDLE_CONNS", "100"},
		"empty database name":    {"DB_NAME", ""},
		"empty database user":    {"DB_USER", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			// DB_NAME and DB_USER are checked after a default is applied, so an
			// empty value falls back rather than failing. Override that by putting
			// whitespace in them, which the loader treats as unset too, so this
			// case is asserted through the validator directly instead.
			if tc.key == "DB_NAME" || tc.key == "DB_USER" {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if tc.key == "DB_NAME" {
					cfg.DBName = ""
				} else {
					cfg.DBUser = ""
				}
				if err := cfg.Validate(); err == nil {
					t.Errorf("Validate accepted an empty %s", tc.key)
				}
				return
			}

			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Errorf("err = nil, want %s=%q rejected", tc.key, tc.value)
			}
		})
	}
}

// The password must never reach the log. RedactedDSN is what the startup line
// prints, so it has to be the masked form and never the real one.
func TestRedactedDSNHidesThePassword(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_PASSWORD", "hunter2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	redacted := cfg.RedactedDSN()
	if strings.Contains(redacted, "hunter2") {
		t.Errorf("RedactedDSN = %q, contains the password", redacted)
	}
	if !strings.Contains(redacted, "****") {
		t.Errorf("RedactedDSN = %q, want a mask", redacted)
	}
	if !strings.Contains(cfg.DSN(), "hunter2") {
		t.Error("DSN does not carry the password, so it cannot connect")
	}
}

// Targets are safe to log because they carry no credentials.
func TestDependenciesIsSafeToLog(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_BROKERS", "broker-1:9092,broker-2:9092")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dependencies := cfg.Dependencies()
	for _, want := range []string{"category=", "media=", "kafka=", "broker-2:9092"} {
		if !strings.Contains(dependencies, want) {
			t.Errorf("Dependencies = %q, want it to include %q", dependencies, want)
		}
	}
}

func TestAddrsBindAllInterfaces(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != ":8082" {
		t.Errorf("Addr = %q, want :8082", cfg.Addr())
	}
	if cfg.GRPCAddr() != ":9092" {
		t.Errorf("GRPCAddr = %q, want :9092", cfg.GRPCAddr())
	}
}
