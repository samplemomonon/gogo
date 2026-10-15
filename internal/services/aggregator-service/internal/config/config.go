// Package config loads and validates the aggregator's configuration from the
// environment.
//
// The shape follows the other services in this repo: a Load function that reads
// every variable, applies a default, and validates, so a misconfigured process
// fails at startup rather than on the first request that happens to need the bad
// value.
package config

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Server.
	AppPort             int
	ReadHeaderTimeout   time.Duration
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	ShutdownTimeout     time.Duration
	MaxRequestBodyBytes int64
	LogLevel            string
	LogFormat           string
	EnableIntrospection bool
	PlaygroundEnabled   bool
	AllowedOrigins      []string
	// MutationTokens are the bearer tokens permitted to execute mutations and
	// subscriptions. Reads are deliberately public.
	//
	// An empty slice does NOT mean "no authentication required" — it means every
	// mutation is refused. See Validate.
	MutationTokens         []string
	RequestTimeout         time.Duration
	DependencyTimeout      time.Duration
	MaxQueryComplexity     int
	MaxQueryDepth          int
	MaxQueryLength         int
	ParserTokenLimit       int
	PersistedQueriesEnable bool

	// Downstream gRPC targets. These are the internal gRPC ports, not the public
	// REST ports: an aggregator is an internal client, so it should not depend on
	// another service's public surface.
	CategoryGRPCTarget string
	MediaGRPCTarget    string
	ProductGRPCTarget  string
	ListingGRPCTarget  string
	SearchGRPCTarget   string

	// Read-side REST targets, used only for the projection status endpoints.
	// Those two are REST-only: neither read service exposes them over gRPC.
	ListingHTTPBase string
	SearchHTTPBase  string

	// Circuit breaker, one per downstream service.
	BreakerFailureThreshold int
	BreakerCooldown         time.Duration
	BreakerHalfOpenProbes   int

	// Retry.
	ReadRetryAttempts int
	ReadRetryBase     time.Duration
	ReadRetryMax      time.Duration

	// Tracing.
	TracingEnabled        bool
	TracingEndpoint       string
	TracingInsecure       bool
	TracingServiceName    string
	TracingServiceVersion string
	TracingEnvironment    string
	TracingSampleRatio    float64
	TracingHeaders        map[string]string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		AppPort:           getEnvInt("APP_PORT", 8085),
		ReadHeaderTimeout: getEnvDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       getEnvDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      getEnvDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout:   getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),

		// GraphQL bodies carry a query document and its variables, not a row, so
		// 1 MiB is already far beyond any legitimate operation. An introspection
		// query against a large schema is the biggest realistic payload.
		MaxRequestBodyBytes: int64(getEnvInt("MAX_REQUEST_BODY_BYTES", 1<<20)),

		LogLevel:  getEnv("LOG_LEVEL", "info"),
		LogFormat: getEnv("LOG_FORMAT", "json"),

		// Introspection is on by default because this service is new and an
		// operator exploring the schema needs it. Disable it in production: the
		// schema is the full published API surface, and introspection hands a
		// caller a machine-readable inventory of it for free.
		EnableIntrospection: getEnvBool("ENABLE_INTROSPECTION", true),

		// The playground executes arbitrary queries against real downstream
		// services, so it defaults off and belongs only in a local dev stack.
		PlaygroundEnabled: getEnvBool("ENABLE_PLAYGROUND", false),
		AllowedOrigins:    getEnvList("ALLOWED_ORIGINS", nil),
		MutationTokens:    getEnvList("MUTATION_TOKENS", nil),

		// The whole-request budget. It must exceed DependencyTimeout, because a
		// single downstream call is expected to fit inside it; if it does not, the
		// per-call timeout is doing nothing and one slow dependency can hold a
		// request open indefinitely.
		RequestTimeout:    getEnvDuration("REQUEST_TIMEOUT", 10*time.Second),
		DependencyTimeout: getEnvDuration("DEPENDENCY_TIMEOUT", 3*time.Second),

		// A page of 20 products with category, media and variants selected costs
		// roughly 120 in the default model, so 250 leaves room for a nested query
		// or two without letting an unbounded alias fan-out through.
		MaxQueryComplexity: getEnvInt("MAX_QUERY_COMPLEXITY", 250),

		// Depth is capped separately from complexity because complexity alone does
		// not stop a deeply nested query: a query can be narrow at every level and
		// still recurse far enough to exhaust the resolver stack.
		MaxQueryDepth:  getEnvInt("MAX_QUERY_DEPTH", 12),
		MaxQueryLength: getEnvInt("MAX_QUERY_LENGTH", 16_000),

		// Bounds parser recursion. Without this a deeply nested query document
		// exhausts the stack during parsing rather than during execution, so
		// validation never gets a chance to reject it.
		ParserTokenLimit: getEnvInt("PARSER_TOKEN_LIMIT", 15_000),

		PersistedQueriesEnable: getEnvBool("ENABLE_PERSISTED_QUERIES", true),

		CategoryGRPCTarget: getEnv("CATEGORY_GRPC_TARGET", "localhost:9090"),
		MediaGRPCTarget:    getEnv("MEDIA_GRPC_TARGET", "localhost:9091"),
		ProductGRPCTarget:  getEnv("PRODUCT_GRPC_TARGET", "localhost:9092"),
		ListingGRPCTarget:  getEnv("LISTING_GRPC_TARGET", "localhost:9093"),
		SearchGRPCTarget:   getEnv("SEARCH_GRPC_TARGET", "localhost:9094"),

		ListingHTTPBase: getEnv("LISTING_HTTP_BASE", "http://localhost:8083"),
		SearchHTTPBase:  getEnv("SEARCH_HTTP_BASE", "http://localhost:8084"),

		// These mirror the thresholds the rest of the stack documents, so an
		// operator reads one set of numbers rather than two.
		BreakerFailureThreshold: getEnvInt("BREAKER_FAILURE_THRESHOLD", 5),
		BreakerCooldown:         getEnvDuration("BREAKER_COOLDOWN", 5*time.Second),
		BreakerHalfOpenProbes:   getEnvInt("BREAKER_HALF_OPEN_PROBES", 1),

		// Three attempts for reads. The request budget is ten seconds and each attempt
		// is bounded at three, so the arithmetic that matters is the retry's total
		// share of the budget: with a 400ms ceiling the loop cannot consume more than
		// about 1.4s of waiting, which leaves room for the attempts themselves. A
		// fourth attempt would add latency without adding a materially better chance of
		// the dependency having recovered.
		ReadRetryAttempts: getEnvInt("READ_RETRY_ATTEMPTS", 3),
		ReadRetryBase:     getEnvDuration("READ_RETRY_BASE_DELAY", 40*time.Millisecond),
		ReadRetryMax:      getEnvDuration("READ_RETRY_MAX_DELAY", 400*time.Millisecond),

		// Tracing is off unless a collector is configured. An enabled exporter with no
		// collector is not a failure, but it is a background retry loop nobody asked
		// for, so the default is off.
		TracingEnabled:        getEnvBool("TRACING_ENABLED", false),
		TracingEndpoint:       getEnv("TRACING_ENDPOINT", ""),
		TracingInsecure:       getEnvBool("TRACING_INSECURE", true),
		TracingServiceName:    getEnv("TRACING_SERVICE_NAME", "aggregator-service"),
		TracingServiceVersion: getEnv("TRACING_SERVICE_VERSION", "dev"),
		TracingEnvironment:    getEnv("TRACING_ENVIRONMENT", ""),
		TracingSampleRatio:    getEnvFloat("TRACING_SAMPLE_RATIO", 1),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate rejects configurations that would misbehave at runtime.
func (c *Config) Validate() error {
	if c.AppPort < 1 || c.AppPort > 65535 {
		return fmt.Errorf("APP_PORT must be between 1 and 65535, got %d", c.AppPort)
	}

	for name, target := range map[string]string{
		"CATEGORY_GRPC_TARGET": c.CategoryGRPCTarget,
		"MEDIA_GRPC_TARGET":    c.MediaGRPCTarget,
		"PRODUCT_GRPC_TARGET":  c.ProductGRPCTarget,
		"LISTING_GRPC_TARGET":  c.ListingGRPCTarget,
		"SEARCH_GRPC_TARGET":   c.SearchGRPCTarget,
	} {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}

	// Mutations are refused unless a token is configured. This is the one place
	// the service is fail-closed, and it is deliberate: the alternative reading of
	// "no tokens configured" is "no authentication required", which is how an
	// endpoint with ten write mutations ends up public because someone removed an
	// environment variable rather than because anyone chose that.
	//
	// An operator who genuinely wants writes open has to say so by configuring an
	// empty-but-present token, which reads differently in a deployment manifest
	// from an absent variable.
	if len(c.MutationTokens) == 0 {
		return fmt.Errorf(
			"MUTATION_TOKENS must list at least one token; an absent value refuses every " +
				"mutation rather than permitting them unauthenticated",
		)
	}
	seen := make(map[string]bool, len(c.MutationTokens))
	for i, token := range c.MutationTokens {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("MUTATION_TOKENS[%d] is empty", i)
		}
		// A short token is not a token. Rejecting it at startup is better than
		// accepting a configuration that looks secured and is not.
		if len(token) < 16 {
			return fmt.Errorf(
				"MUTATION_TOKENS[%d] is %d characters; use at least 16 so the value is not "+
					"guessable by brute force",
				i, len(token),
			)
		}
		if seen[token] {
			return fmt.Errorf("MUTATION_TOKENS[%d] duplicates an earlier token", i)
		}
		seen[token] = true
	}

	if c.RequestTimeout <= c.DependencyTimeout {
		return fmt.Errorf(
			"REQUEST_TIMEOUT (%s) must exceed DEPENDENCY_TIMEOUT (%s); "+
				"otherwise a single downstream call cannot finish inside the request budget",
			c.RequestTimeout, c.DependencyTimeout,
		)
	}

	if c.MaxQueryComplexity < 1 {
		return fmt.Errorf("MAX_QUERY_COMPLEXITY must be at least 1, got %d", c.MaxQueryComplexity)
	}
	if c.MaxQueryDepth < 1 {
		return fmt.Errorf("MAX_QUERY_DEPTH must be at least 1, got %d", c.MaxQueryDepth)
	}
	if c.MaxQueryLength < 1 {
		return fmt.Errorf("MAX_QUERY_LENGTH must be at least 1, got %d", c.MaxQueryLength)
	}
	if c.ParserTokenLimit < 1 {
		return fmt.Errorf("PARSER_TOKEN_LIMIT must be at least 1, got %d", c.ParserTokenLimit)
	}
	if c.MaxRequestBodyBytes < 1 {
		return fmt.Errorf("MAX_REQUEST_BODY_BYTES must be at least 1, got %d", c.MaxRequestBodyBytes)
	}

	if c.BreakerFailureThreshold < 1 {
		return fmt.Errorf("BREAKER_FAILURE_THRESHOLD must be at least 1, got %d", c.BreakerFailureThreshold)
	}
	if c.BreakerCooldown <= 0 {
		return fmt.Errorf("BREAKER_COOLDOWN must be positive, got %s", c.BreakerCooldown)
	}
	if c.BreakerHalfOpenProbes < 1 {
		return fmt.Errorf("BREAKER_HALF_OPEN_PROBES must be at least 1, got %d", c.BreakerHalfOpenProbes)
	}

	if c.ReadRetryAttempts < 1 {
		return fmt.Errorf("READ_RETRY_ATTEMPTS must be at least 1, got %d", c.ReadRetryAttempts)
	}
	if c.ReadRetryBase < 0 {
		return fmt.Errorf("READ_RETRY_BASE_DELAY must not be negative, got %s", c.ReadRetryBase)
	}
	if c.ReadRetryMax < c.ReadRetryBase {
		return fmt.Errorf("READ_RETRY_MAX_DELAY (%s) must be at least READ_RETRY_BASE_DELAY (%s)",
			c.ReadRetryMax, c.ReadRetryBase)
	}

	// The retry schedule has to fit inside the request budget. A ceiling larger than the
	// budget means a single backoff can outlast a whole request, which turns the retry
	// into pure latency on a call that was already going to time out.
	if c.ReadRetryMax > c.RequestTimeout/2 {
		return fmt.Errorf(
			"READ_RETRY_MAX_DELAY (%s) must be at most half of REQUEST_TIMEOUT (%s); "+
				"a single backoff longer than that leaves no budget for the attempt it precedes",
			c.ReadRetryMax, c.RequestTimeout,
		)
	}

	// The NaN check is redundant with the one in getEnvFloat for values read from the
	// environment, and is kept because Config can also be constructed directly. It also
	// documents why: a bare range comparison would not catch it.
	if math.IsNaN(c.TracingSampleRatio) || math.IsInf(c.TracingSampleRatio, 0) ||
		c.TracingSampleRatio < 0 || c.TracingSampleRatio > 1 {
		return fmt.Errorf("TRACING_SAMPLE_RATIO must be between 0 and 1, got %v", c.TracingSampleRatio)
	}

	// Off in production unless the operator asked for it. Failing loud here beats
	// discovering it from a monitoring alert.
	if !c.EnableIntrospection && strings.EqualFold(c.LogLevel, "debug") {
		return fmt.Errorf("ENABLE_INTROSPECTION is false but LOG_LEVEL is debug; " +
			"introspection is usually wanted while debugging a schema")
	}

	if _, err := parseLevel(c.LogLevel); err != nil {
		return err
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		return fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat)
	}

	return nil
}

// Addr is the HTTP listen address.
func (c *Config) Addr() string { return fmt.Sprintf(":%d", c.AppPort) }

// SampleEverythingInProduction reports whether traces are being fully sampled outside
// development.
//
// It is a question rather than a rejection because full sampling is a legitimate choice
// during an investigation, and an operator who turns it on should be told what it costs
// rather than prevented from doing it. A service that fans out five ways produces
// several spans per request, so a full sample rate multiplies.
func (c *Config) SampleEverythingInProduction() bool {
	return c.TracingEnabled && c.TracingSampleRatio >= 1 && !c.IsDevelopment()
}

// IsDevelopment reports whether the deployment looks like a local or development one.
func (c *Config) IsDevelopment() bool {
	return strings.EqualFold(c.TracingEnvironment, "development") ||
		strings.EqualFold(c.TracingEnvironment, "dev") ||
		strings.EqualFold(c.TracingEnvironment, "local")
}

// Dependencies renders the downstream targets for the startup log. gRPC targets
// are not secret, so they are logged as configured.
func (c *Config) Dependencies() string {
	return fmt.Sprintf(
		"category=%s media=%s product=%s listing=%s search=%s",
		c.CategoryGRPCTarget, c.MediaGRPCTarget, c.ProductGRPCTarget,
		c.ListingGRPCTarget, c.SearchGRPCTarget,
	)
}

// OriginAllowed reports whether origin may use the browser transport.
//
// A request with no Origin header is not a browser request, so it is allowed:
// curl and server-to-server clients omit it entirely, and rejecting those would
// break every non-browser caller. The wildcard matches the repo's other services,
// which carry no origin check of their own.
func (c *Config) OriginAllowed(origin string) bool {
	if origin == "" || slices.Contains(c.AllowedOrigins, "*") {
		return true
	}
	return slices.Contains(c.AllowedOrigins, origin)
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// getEnvList splits a comma-separated value, dropping empty entries so a trailing
// comma in a compose file does not produce a literal empty origin.
func getEnvList(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func getEnvInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

// getEnvBool accepts the spellings that appear in compose files and deployment
// manifests in practice. strconv.ParseBool would reject "yes" and "on", which are
// both common, and silently defaulting those is worse than accepting them.
func getEnvBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getEnvFloat(key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	// ParseFloat accepts "NaN", "Inf" and "-Inf", and every comparison against them is
	// false. A range check written as `v < lo || v > hi` therefore lets all three
	// through, and a NaN sample ratio reaches the tracer provider, where it silently
	// produces an undefined sampling decision rather than a default. Rejected here so
	// the range check downstream can stay the simple form it reads as.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return v
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func parseLevel(s string) (any, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "info", "warn", "error":
		return nil, nil
	default:
		return nil, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", s)
	}
}
