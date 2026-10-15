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
		"DB_CONNECT_TIMEOUT", "APP_PORT", "GRPC_PORT", "GRPC_MAX_MESSAGE_SIZE",
		"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT",
		"KAFKA_BROKERS", "KAFKA_CLIENT_ID", "KAFKA_CONSUMER_GROUP", "KAFKA_TOPIC",
		"KAFKA_DEAD_LETTER_TOPIC", "KAFKA_MAX_POLL_RECORDS", "KAFKA_POLL_TIMEOUT",
		"KAFKA_RETRY_BACKOFF", "CONSUMER_ENABLED", "PROCESSED_EVENT_RETENTION",
		"PRUNE_INTERVAL",
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

	// The listing service must not collide with the write side, the other
	// services, or Kafka on any of the conventional ports.
	if cfg.AppPort != "8083" {
		t.Errorf("APP_PORT = %q, want 8083", cfg.AppPort)
	}
	if cfg.GRPCPort != "9093" {
		t.Errorf("GRPC_PORT = %q, want 9093", cfg.GRPCPort)
	}
	if cfg.KafkaTopic != "product.events.v1" {
		t.Errorf("KAFKA_TOPIC = %q, want product.events.v1", cfg.KafkaTopic)
	}
	if cfg.KafkaDeadLetterTopic != "product.events.v1.dlq" {
		t.Errorf("KAFKA_DEAD_LETTER_TOPIC = %q, want product.events.v1.dlq", cfg.KafkaDeadLetterTopic)
	}
	if !cfg.ConsumerEnabled {
		t.Error("CONSUMER_ENABLED = false, want the consumer on by default")
	}
	if cfg.DBName != "listing_db" {
		t.Errorf("DB_NAME = %q, want listing_db", cfg.DBName)
	}
}

// The service must start with no configuration at all, which is what lets it come
// up before the broker is ready.
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
// first poll, so it is caught at startup instead.
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

// Without a dead-letter topic an unapplicable record has nowhere to go: dropping
// it loses it, and retrying it forever stops its partition moving. Both are worse
// than refusing to start.
//
// A blank environment variable counts as unset and falls back to the default, so
// the only way to reach an empty topic is through the validator, which is what
// this asserts.
func TestDeadLetterTopicIsRequiredWithTheConsumer(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg.KafkaDeadLetterTopic = ""
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty dead-letter topic with the consumer enabled")
	}
	if !strings.Contains(err.Error(), "KAFKA_DEAD_LETTER_TOPIC") {
		t.Errorf("err = %q, want it to name the variable", err)
	}
}

// A blank environment variable is treated as unset, so it must fall back to the
// default rather than produce an empty value.
func TestBlankValuesFallBackToDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_CONSUMER_GROUP", "  ")
	t.Setenv("KAFKA_TOPIC", "  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KafkaConsumerGroup != "product-listing-service" {
		t.Errorf("consumer group = %q, want the default", cfg.KafkaConsumerGroup)
	}
	if cfg.KafkaTopic != "product.events.v1" {
		t.Errorf("topic = %q, want the default", cfg.KafkaTopic)
	}
}

// The same topic for both would make the consumer read back the records it just
// rejected.
func TestDeadLetterTopicMustDifferFromTheMainTopic(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_TOPIC", "product.events.v1")
	t.Setenv("KAFKA_DEAD_LETTER_TOPIC", "product.events.v1")

	if _, err := Load(); err == nil {
		t.Error("err = nil, want the dead-letter topic to differ from the main one")
	}
}

// Running the read API on its own is legitimate, for instance while rebuilding the
// projection by hand, so the dead-letter requirement lifts with the consumer.
func TestConsumerCanBeDisabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("CONSUMER_ENABLED", "false")
	t.Setenv("KAFKA_DEAD_LETTER_TOPIC", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ConsumerEnabled {
		t.Error("CONSUMER_ENABLED = true, want it disabled")
	}
}

func TestConsumerEnabledAcceptsTheUsualSpellings(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		clearEnv(t)
		t.Setenv("CONSUMER_ENABLED", value)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("CONSUMER_ENABLED=%s: %v", value, err)
		}
		if !cfg.ConsumerEnabled {
			t.Errorf("CONSUMER_ENABLED=%s was read as false", value)
		}
	}
	for _, value := range []string{"0", "false", "no", "off"} {
		clearEnv(t)
		t.Setenv("CONSUMER_ENABLED", value)
		t.Setenv("KAFKA_DEAD_LETTER_TOPIC", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("CONSUMER_ENABLED=%s: %v", value, err)
		}
		if cfg.ConsumerEnabled {
			t.Errorf("CONSUMER_ENABLED=%s was read as true", value)
		}
	}
	clearEnv(t)
	t.Setenv("CONSUMER_ENABLED", "maybe")
	if _, err := Load(); err == nil {
		t.Error("err = nil, want an unparseable boolean to be rejected")
	}
}

// A retention window shorter than a minute would prune event ids the broker could
// still legitimately redeliver, which defeats the deduplication the projection
// depends on.
func TestProcessedEventRetentionHasAFloor(t *testing.T) {
	clearEnv(t)
	t.Setenv("PROCESSED_EVENT_RETENTION", "10s")

	_, err := Load()
	if err == nil {
		t.Error("err = nil, want a retention window below a minute to be rejected")
	}
}

func TestConsumerSettingsAreConfigurable(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_MAX_POLL_RECORDS", "500")
	t.Setenv("KAFKA_POLL_TIMEOUT", "2s")
	t.Setenv("KAFKA_RETRY_BACKOFF", "10s")
	t.Setenv("KAFKA_CONSUMER_GROUP", "listing-v2")
	t.Setenv("PROCESSED_EVENT_RETENTION", "48h")
	t.Setenv("PRUNE_INTERVAL", "15m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KafkaMaxPollRecords != 500 {
		t.Errorf("KAFKA_MAX_POLL_RECORDS = %d, want 500", cfg.KafkaMaxPollRecords)
	}
	if cfg.KafkaPollTimeout != 2*time.Second {
		t.Errorf("KAFKA_POLL_TIMEOUT = %s, want 2s", cfg.KafkaPollTimeout)
	}
	if cfg.KafkaRetryBackoff != 10*time.Second {
		t.Errorf("KAFKA_RETRY_BACKOFF = %s, want 10s", cfg.KafkaRetryBackoff)
	}
	if cfg.KafkaConsumerGroup != "listing-v2" {
		t.Errorf("KAFKA_CONSUMER_GROUP = %q, want listing-v2", cfg.KafkaConsumerGroup)
	}
	if cfg.ProcessedEventRetention != 48*time.Hour {
		t.Errorf("PROCESSED_EVENT_RETENTION = %s, want 48h", cfg.ProcessedEventRetention)
	}
	if cfg.PruneInterval != 15*time.Minute {
		t.Errorf("PRUNE_INTERVAL = %s, want 15m", cfg.PruneInterval)
	}
}

func TestInvalidValuesAreRejected(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"non-numeric poll records": {"KAFKA_MAX_POLL_RECORDS", "many"},
		"zero poll records":        {"KAFKA_MAX_POLL_RECORDS", "0"},
		"bad poll timeout":         {"KAFKA_POLL_TIMEOUT", "soon"},
		"zero retry backoff":       {"KAFKA_RETRY_BACKOFF", "0s"},
		"non-numeric port":         {"APP_PORT", "http"},
		"port out of range":        {"APP_PORT", "70000"},
		"unknown ssl mode":         {"DB_SSLMODE", "maybe"},
		"unknown log format":       {"LOG_FORMAT", "yaml"},
		"small message limit":      {"GRPC_MAX_MESSAGE_SIZE", "16"},
		"idle exceeds max open":    {"DB_MAX_IDLE_CONNS", "100"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.key, tc.value)

			if _, err := Load(); err == nil {
				t.Errorf("err = nil, want %s=%q rejected", tc.key, tc.value)
			}
		})
	}
}

// A blank environment value counts as unset, so the ways to reach an empty
// identity are only reachable through the validator.
func TestValidateRejectsEmptyIdentity(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, tc := range []struct {
		name   string
		break_ func(*Config)
		want   string
	}{
		{"empty database name", func(c *Config) { c.DBName = "" }, "DB_NAME"},
		{"empty database user", func(c *Config) { c.DBUser = "" }, "DB_USER"},
		{"empty consumer group", func(c *Config) { c.KafkaConsumerGroup = "" }, "KAFKA_CONSUMER_GROUP"},
		{"empty topic", func(c *Config) { c.KafkaTopic = "" }, "KAFKA_TOPIC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := *cfg
			tc.break_(&broken)

			err := broken.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to name %s", err, tc.want)
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

// Broker targets carry no credentials, so they are safe in the startup line.
func TestDependenciesIsSafeToLog(t *testing.T) {
	clearEnv(t)
	t.Setenv("KAFKA_BROKERS", "broker-1:9092,broker-2:9092")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dependencies := cfg.Dependencies()
	if !strings.Contains(dependencies, "kafka=") || !strings.Contains(dependencies, "broker-2:9092") {
		t.Errorf("Dependencies = %q, want it to include the broker list", dependencies)
	}
}

func TestAddrsBindAllInterfaces(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != ":8083" {
		t.Errorf("Addr = %q, want :8083", cfg.Addr())
	}
	if cfg.GRPCAddr() != ":9093" {
		t.Errorf("GRPCAddr = %q, want :9093", cfg.GRPCAddr())
	}
}
