package config

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests in this file concentrate on the resilience and tracing settings, because
// those are the ones where a plausible-looking value produces a service that behaves
// badly in a way nothing else reports: a retry budget that cannot fit its own backoff,
// a breaker that opens on healthy traffic, a sample rate that dumps full volume into a
// collector an operator just scaled down.

// validConfig is the config a deployment gets with no environment set at all.
//
// Built through Load rather than as a literal, so it cannot drift out of sync with the
// real defaults as new required settings are added. A hand-written literal would fail
// here the first time Validate grew a new requirement, and every failure in the tests
// below would then be about the baseline rather than the setting under test.
func validConfig(t *testing.T) Config {
	t.Helper()

	// Every variable this package reads is cleared, so the ambient environment cannot
	// change what the baseline is.
	for _, key := range []string{
		"BREAKER_FAILURE_THRESHOLD", "BREAKER_COOLDOWN", "BREAKER_HALF_OPEN_PROBES",
		"READ_RETRY_ATTEMPTS", "READ_RETRY_BASE_DELAY", "READ_RETRY_MAX_DELAY",
		"TRACING_ENABLED", "TRACING_SAMPLE_RATIO", "TRACING_ENDPOINT",
		"TRACING_ENVIRONMENT", "REQUEST_TIMEOUT", "DEPENDENCY_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("the default config does not load: %v", err)
	}
	return *cfg
}

func TestTheValidBaselineIsAccepted(t *testing.T) {
	// Every single-setting test below perturbs this. If it does not validate, the
	// failures in those tests would be about the baseline rather than the setting.
	cfg := validConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the baseline config does not validate: %v", err)
	}

	// And the shipped defaults are the ones the resilience design assumes: three
	// attempts for reads, a breaker that opens on five consecutive failures, and
	// tracing off. If these drift, the reasoning in the breaker and retry packages
	// about budget arithmetic stops applying.
	if cfg.ReadRetryAttempts != 3 {
		t.Errorf("READ_RETRY_ATTEMPTS defaults to %d, want 3", cfg.ReadRetryAttempts)
	}
	if cfg.BreakerFailureThreshold != 5 {
		t.Errorf("BREAKER_FAILURE_THRESHOLD defaults to %d, want 5", cfg.BreakerFailureThreshold)
	}
	if cfg.TracingEnabled {
		t.Error("TRACING_ENABLED defaults to true, want false")
	}
}

func TestRetrySettingsAreValidated(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"zero attempts":     func(c *Config) { c.ReadRetryAttempts = 0 },
		"negative attempts": func(c *Config) { c.ReadRetryAttempts = -1 },
		"negative base":     func(c *Config) { c.ReadRetryBase = -time.Millisecond },
		"max below base":    func(c *Config) { c.ReadRetryMax = time.Millisecond },
	} {
		cfg := validConfig(t)
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted, want rejected: an unusable retry schedule is worse than none", name)
		}
	}
}

func TestTheRetryCeilingMustFitInsideTheRequestBudget(t *testing.T) {
	// The interaction that matters most. A backoff window longer than half the request
	// budget means a single wait can outlast the whole request: the retry never happens
	// and the latency is added to a call that was going to time out anyway.
	cfg := validConfig(t)
	cfg.ReadRetryMax = 6 * time.Second // over half of the 10s budget

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a backoff ceiling larger than half the request budget was accepted")
	}
	// The message has to name both settings, because the fix is not obvious from
	// whichever one the operator happened to change.
	for _, want := range []string{"READ_RETRY_MAX_DELAY", "REQUEST_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %s: %v", want, err)
		}
	}
}

func TestARetryCeilingAtExactlyHalfTheBudgetIsAccepted(t *testing.T) {
	// The boundary, pinned so a later change to a strict inequality is caught. Half is
	// the largest ceiling that still leaves room for the attempt the wait precedes.
	cfg := validConfig(t)
	cfg.RequestTimeout = 10 * time.Second
	cfg.ReadRetryMax = 5 * time.Second

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a ceiling of exactly half the budget was rejected: %v", err)
	}
}

func TestTheSampleRatioIsValidated(t *testing.T) {
	for _, ratio := range []float64{-0.1, 1.1, 2, math.NaN(), math.Inf(1), math.Inf(-1)} {
		cfg := validConfig(t)
		cfg.TracingSampleRatio = ratio
		if err := cfg.Validate(); err == nil {
			t.Errorf("TRACING_SAMPLE_RATIO=%v was accepted, want rejected", ratio)
		}
	}

	for _, ratio := range []float64{0, 0.001, 0.5, 1} {
		cfg := validConfig(t)
		cfg.TracingSampleRatio = ratio
		if err := cfg.Validate(); err != nil {
			t.Errorf("TRACING_SAMPLE_RATIO=%v was rejected: %v", ratio, err)
		}
	}
}

func TestFullSamplingOutsideDevelopmentIsFlagged(t *testing.T) {
	// A warning rather than a rejection, because full sampling is a legitimate choice
	// during an investigation. But the aggregator fans out five ways, so it emits several
	// spans per request and a full sample rate multiplies the export volume by that
	// factor — which an operator enabling tracing locally does not expect to hit prod.
	for name, tc := range map[string]struct {
		enabled  bool
		ratio    float64
		env      string
		wantWarn bool
	}{
		"enabled, full, production":    {true, 1, "production", true},
		"enabled, full, staging":       {true, 1, "staging", true},
		"enabled, full, development":   {true, 1, "development", false},
		"enabled, full, dev":           {true, 1, "dev", false},
		"enabled, full, local":         {true, 1, "local", false},
		"enabled, full, unset":         {true, 1, "", true},
		"enabled, sampled, production": {true, 0.1, "production", false},
		"disabled, full, production":   {false, 1, "production", false},
	} {
		cfg := validConfig(t)
		cfg.TracingEnabled = tc.enabled
		cfg.TracingSampleRatio = tc.ratio
		cfg.TracingEnvironment = tc.env

		if got := cfg.SampleEverythingInProduction(); got != tc.wantWarn {
			t.Errorf("%s: SampleEverythingInProduction() = %v, want %v", name, got, tc.wantWarn)
		}
	}
}

func TestIsDevelopmentRecognisesItsSpellings(t *testing.T) {
	// Case-insensitive because the value comes from an environment variable, where
	// "Development" and "DEVELOPMENT" are the same intent written three ways and a
	// missed warning is a silently expensive deployment.
	for _, env := range []string{"development", "Development", "DEVELOPMENT", "dev", "DEV", "local", "Local"} {
		cfg := validConfig(t)
		cfg.TracingEnvironment = env
		if !cfg.IsDevelopment() {
			t.Errorf("environment %q was not recognised as development", env)
		}
	}

	for _, env := range []string{"production", "staging", "prod", "", "locality", "development-ish"} {
		cfg := validConfig(t)
		cfg.TracingEnvironment = env
		if cfg.IsDevelopment() {
			t.Errorf("environment %q was treated as development", env)
		}
	}
}

func TestGetEnvFloatFallsBackRatherThanFailing(t *testing.T) {
	// Consistent with the other getters: an unparseable value is a typo in a
	// deployment manifest, and refusing to start over one would be worse than running
	// with the documented default. The alternative — a hard failure — turns a
	// misspelled environment variable into an outage.
	t.Setenv("TEST_FLOAT", "0.25")
	if got := getEnvFloat("TEST_FLOAT", 1); got != 0.25 {
		t.Errorf("got %v, want 0.25", got)
	}

	// "NaN", "Inf" and "-Inf" parse successfully, and every comparison against them is
	// false. A range check written as `v < lo || v > hi` would therefore let them all
	// through to the sampler, which makes an undefined sampling decision rather than
	// falling back. Each is asserted explicitly for that reason.
	for _, raw := range []string{"", "   ", "not a number", "NaN", "Inf", "-Inf", "+Inf"} {
		t.Setenv("TEST_FLOAT", raw)
		if got := getEnvFloat("TEST_FLOAT", 0.5); got != 0.5 {
			t.Errorf("raw %q gave %v, want the 0.5 fallback", raw, got)
		}
	}

	t.Setenv("TEST_FLOAT", "0.5")
	if got := getEnvFloat("TEST_ABSENT_FLOAT", 0.75); got != 0.75 {
		t.Errorf("an unset variable gave %v, want the fallback", got)
	}
}

func TestLoadReadsTheResilienceSettingsFromTheEnvironment(t *testing.T) {
	// The wiring, rather than the parsing: a setting that Validate checks but Load
	// never reads would pass every unit test and do nothing in a deployment.
	t.Setenv("BREAKER_FAILURE_THRESHOLD", "7")
	t.Setenv("BREAKER_COOLDOWN", "12s")
	t.Setenv("BREAKER_HALF_OPEN_PROBES", "3")
	t.Setenv("READ_RETRY_ATTEMPTS", "5")
	t.Setenv("READ_RETRY_BASE_DELAY", "25ms")
	t.Setenv("READ_RETRY_MAX_DELAY", "500ms")
	t.Setenv("TRACING_ENABLED", "true")
	t.Setenv("TRACING_ENDPOINT", "collector:4317")
	t.Setenv("TRACING_SERVICE_VERSION", "9.9.9")
	t.Setenv("TRACING_ENVIRONMENT", "staging")
	t.Setenv("TRACING_SAMPLE_RATIO", "0.2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.BreakerFailureThreshold != 7 || cfg.BreakerCooldown != 12*time.Second || cfg.BreakerHalfOpenProbes != 3 {
		t.Errorf("breaker settings not read: %d/%s/%d", cfg.BreakerFailureThreshold, cfg.BreakerCooldown, cfg.BreakerHalfOpenProbes)
	}
	if cfg.ReadRetryAttempts != 5 || cfg.ReadRetryBase != 25*time.Millisecond || cfg.ReadRetryMax != 500*time.Millisecond {
		t.Errorf("retry settings not read: %d/%s/%s", cfg.ReadRetryAttempts, cfg.ReadRetryBase, cfg.ReadRetryMax)
	}
	if !cfg.TracingEnabled {
		t.Error("TRACING_ENABLED was not read")
	}
	if cfg.TracingEndpoint != "collector:4317" || cfg.TracingServiceVersion != "9.9.9" || cfg.TracingEnvironment != "staging" {
		t.Errorf("tracing identity not read: %q/%q/%q", cfg.TracingEndpoint, cfg.TracingServiceVersion, cfg.TracingEnvironment)
	}
	if cfg.TracingSampleRatio != 0.2 {
		t.Errorf("TRACING_SAMPLE_RATIO = %v, want 0.2", cfg.TracingSampleRatio)
	}
}

func TestTracingIsOffUnlessAskedFor(t *testing.T) {
	// An enabled exporter with no collector is a background retry loop nobody asked for.
	// Off has to be the default, not "on but pointing nowhere".
	//
	// The unset is skipped rather than cleared: t.Setenv cannot unset, so a variable
	// already present in the ambient environment would make this assert a default that
	// is not in effect.
	if _, present := os.LookupEnv("TRACING_ENABLED"); present {
		t.Skip("TRACING_ENABLED is set in this environment")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TracingEnabled {
		t.Fatal("tracing is on by default")
	}
}
