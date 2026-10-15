package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every variable this package reads, so a test starts from the
// documented defaults regardless of what the developer has exported.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE",
		"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME", "DB_CONNECT_TIMEOUT",
		"KAFKA_BROKERS", "KAFKA_CLIENT_ID", "KAFKA_CONSUMER_GROUP", "KAFKA_TOPIC",
		"KAFKA_DEAD_LETTER_TOPIC", "KAFKA_MAX_POLL_RECORDS", "KAFKA_POLL_TIMEOUT",
		"KAFKA_RETRY_BACKOFF", "CONSUMER_ENABLED", "PROCESSED_EVENT_RETENTION", "PRUNE_INTERVAL",
		"APP_PORT", "GRPC_PORT", "GRPC_MAX_MESSAGE_SIZE",
		"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT",
	} {
		t.Setenv(key, "")
	}
}

// The search service has to default to its own database and its own ports, not the
// listing service's. Sharing a port would make one of them unreachable, and
// sharing a database would break the one-database-per-service rule the split rests
// on.
func TestDefaultsAreDistinctFromTheListingService(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := map[string]struct{ got, want string }{
		"app port":       {cfg.AppPort, "8084"},
		"grpc port":      {cfg.GRPCPort, "9094"},
		"database":       {cfg.DBName, "search_db"},
		"db user":        {cfg.DBUser, "search_user"},
		"client id":      {cfg.KafkaClientID, "product-search-service"},
		"consumer group": {cfg.KafkaConsumerGroup, "product-search-service"},
	}
	for name, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", name, tc.got, tc.want)
		}
	}
}

// The consumer group is what makes the split work. Two services in one group would
// have the partitions split between them, so each would index half the catalogue
// and serve half the searches silently.
func TestConsumerGroupDiffersFromTheListingService(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	const listingGroup = "product-listing-service"
	if cfg.KafkaConsumerGroup == listingGroup {
		t.Errorf("consumer group = %q, must differ from the listing service's or the two "+
			"would split the partitions between them and each index only half the topic",
			cfg.KafkaConsumerGroup)
	}
	// Both read the same topic, which is what keeps the two read models in step.
	if cfg.KafkaTopic != "product.events.v1" {
		t.Errorf("topic = %q, want product.events.v1", cfg.KafkaTopic)
	}
}

func TestValidateRejectsInconsistentValues(t *testing.T) {
	cases := map[string]map[string]string{
		"dead letter is the topic": {
			"KAFKA_TOPIC": "events", "KAFKA_DEAD_LETTER_TOPIC": "events",
		},
		"broker without a port": {
			"KAFKA_BROKERS": "kafka",
		},
		"idle conns above open conns": {
			"DB_MAX_OPEN_CONNS": "5", "DB_MAX_IDLE_CONNS": "10",
		},
		"retention below a minute": {
			"PROCESSED_EVENT_RETENTION": "10s",
		},
		"port out of range": {
			"APP_PORT": "70000",
		},
		"unknown ssl mode": {
			"DB_SSLMODE": "sometimes",
		},
		"unknown log format": {
			"LOG_FORMAT": "xml",
		},
		"duration is not one": {
			"HTTP_READ_TIMEOUT": "soon",
		},
		"boolean is not one": {
			"CONSUMER_ENABLED": "maybe",
		},
		"integer is not one": {
			"KAFKA_MAX_POLL_RECORDS": "lots",
		},
	}

	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			for key, value := range env {
				t.Setenv(key, value)
			}

			if _, err := Load(); err == nil {
				t.Error("err = nil, want the configuration to be rejected")
			}
		})
	}
}

// getEnv deliberately collapses an empty value into the default, so "the operator
// set it to nothing" and "the operator did not set it" are the same thing at the
// environment boundary. That makes two of the validation rules impossible to reach
// through the environment, so they are checked against Validate directly.
func TestValidateRejectsValuesTheEnvironmentCannotExpress(t *testing.T) {
	clearEnv(t)

	base, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Run("no dead letter topic while consuming", func(t *testing.T) {
		cfg := *base
		cfg.ConsumerEnabled = true
		cfg.KafkaDeadLetterTopic = ""

		err := cfg.Validate()
		if err == nil {
			t.Fatal("err = nil, want it rejected: an unapplicable record would hold its partition")
		}
		if !strings.Contains(err.Error(), "KAFKA_DEAD_LETTER_TOPIC") {
			t.Errorf("err = %v, want it to name the variable to set", err)
		}
	})

	t.Run("empty consumer group", func(t *testing.T) {
		cfg := *base
		cfg.KafkaConsumerGroup = ""

		if err := cfg.Validate(); err == nil {
			t.Error("err = nil, want an empty consumer group rejected")
		}
	})
}

// Running the API without consuming is a supported mode, and it is the only way to
// get a dead-letter-less configuration past the check above.
func TestConsumerCanBeDisabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("CONSUMER_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ConsumerEnabled {
		t.Error("ConsumerEnabled = true, want the operator's false to be honoured")
	}
}

func TestDSNIsRedactedForLogging(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_PASSWORD", "hunter2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !strings.Contains(cfg.DSN(), "hunter2") {
		t.Error("the real DSN must carry the password, or it cannot connect")
	}
	if strings.Contains(cfg.RedactedDSN(), "hunter2") {
		t.Error("the redacted DSN leaks the password")
	}
	if !strings.Contains(cfg.RedactedDSN(), "****") {
		t.Errorf("redacted DSN = %q, want the password masked", cfg.RedactedDSN())
	}
}

func TestAddrs(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != ":8084" {
		t.Errorf("Addr = %q, want :8084", cfg.Addr())
	}
	if cfg.GRPCAddr() != ":9094" {
		t.Errorf("GRPCAddr = %q, want :9094", cfg.GRPCAddr())
	}
}

func TestDurationsAreParsed(t *testing.T) {
	clearEnv(t)
	t.Setenv("SHUTDOWN_TIMEOUT", "45s")
	t.Setenv("PROCESSED_EVENT_RETENTION", "2h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ShutdownTimeout != 45*time.Second {
		t.Errorf("shutdown = %s, want 45s", cfg.ShutdownTimeout)
	}
	if cfg.ProcessedEventRetention != 2*time.Hour {
		t.Errorf("retention = %s, want 2h", cfg.ProcessedEventRetention)
	}
}

// A stray blank value in the broker list must not become a broker named "".
func TestParseBrokersDiscardsBlanks(t *testing.T) {
	brokers := parseBrokers("a:1, ,b:2,")
	if len(brokers) != 2 || brokers[0] != "a:1" || brokers[1] != "b:2" {
		t.Errorf("brokers = %v, want [a:1 b:2]", brokers)
	}
}

// Guard against a variable being read but never cleared by the test helper, which
// would make the whole suite order-dependent and dependent on the developer's shell.
//
// The helper blanks rather than unsets, and getEnv treats a blank value as absent,
// so a leftover non-empty value is the only thing that indicates a real gap.
func TestClearEnvCoversTheDocumentedVariables(t *testing.T) {
	clearEnv(t)

	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" {
			continue
		}
		switch {
		case strings.HasPrefix(name, "DB_"), strings.HasPrefix(name, "KAFKA_"),
			strings.HasPrefix(name, "HTTP_"), strings.HasPrefix(name, "LOG_"),
			name == "APP_PORT", name == "GRPC_PORT", name == "CONSUMER_ENABLED",
			name == "PROCESSED_EVENT_RETENTION", name == "PRUNE_INTERVAL",
			name == "SHUTDOWN_TIMEOUT":
			t.Errorf("%s is set to %q but not cleared by the test helper", name, value)
		}
	}
}
