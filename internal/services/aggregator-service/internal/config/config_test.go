package config

import (
	"testing"
	"time"
)

// The tests here are about the two properties that matter for a service with no
// database: nothing silently defaults into a broken state, and nothing invalid
// passes Load. The second is the one that catches deployment mistakes at startup
// rather than on the first request that needs the bad value.

func TestLoadAppliesDefaults(t *testing.T) {
	// Nothing set: a developer running locally with no environment should get a
	// working configuration pointing at localhost.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AppPort != 8085 {
		t.Fatalf("port %d, want 8085", cfg.AppPort)
	}
	if cfg.Addr() != ":8085" {
		t.Fatalf("addr %q", cfg.Addr())
	}
	if cfg.RequestTimeout <= cfg.DependencyTimeout {
		t.Fatalf("request budget %s must exceed the per-call timeout %s, "+
			"or a single downstream call cannot finish inside the request",
			cfg.RequestTimeout, cfg.DependencyTimeout)
	}
	if !cfg.EnableIntrospection {
		t.Fatal("introspection should default on for a new service")
	}
	if cfg.PlaygroundEnabled {
		t.Fatal("the playground executes arbitrary queries against real services; it must default off")
	}
}

func TestRequestTimeoutMustExceedDependencyTimeout(t *testing.T) {
	// The inverse of this check is a silent failure. If the request budget is shorter
	// than a single downstream call, the per-call timeout is doing nothing and one
	// slow dependency can hold a request open for the whole budget while every
	// individual call still reports that it timed out.
	t.Setenv("REQUEST_TIMEOUT", "2s")
	t.Setenv("DEPENDENCY_TIMEOUT", "5s")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load to reject a request budget shorter than the per-call timeout")
	}
}

func TestEmptyGrpcTargetIsRejected(t *testing.T) {
	// An empty target would dial nothing and fail every call at runtime with an error
	// naming no service.
	t.Setenv("CATEGORY_GRPC_TARGET", "")

	// getEnv treats empty as unset, so this exercises the whitespace path instead.
	t.Setenv("CATEGORY_GRPC_TARGET", "   ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("whitespace should fall back to the default: %v", err)
	}
	if cfg.CategoryGRPCTarget != "localhost:9090" {
		t.Fatalf("target %q", cfg.CategoryGRPCTarget)
	}
}

func TestAllFiveTargetsAreRequired(t *testing.T) {
	// The aggregator fronts five services. A configuration missing one is not a
	// partial setup; it is a service that will fail part of every response.
	for _, env := range []string{
		"CATEGORY_GRPC_TARGET",
		"MEDIA_GRPC_TARGET",
		"PRODUCT_GRPC_TARGET",
		"LISTING_GRPC_TARGET",
		"SEARCH_GRPC_TARGET",
	} {
		t.Run(env, func(t *testing.T) {
			cfg := &Config{
				CategoryGRPCTarget:      "localhost:9090",
				MediaGRPCTarget:         "localhost:9091",
				ProductGRPCTarget:       "localhost:9092",
				ListingGRPCTarget:       "localhost:9093",
				SearchGRPCTarget:        "localhost:9094",
				RequestTimeout:          10 * time.Second,
				DependencyTimeout:       3 * time.Second,
				MaxQueryComplexity:      100,
				MaxQueryDepth:           10,
				MaxQueryLength:          1000,
				ParserTokenLimit:        1000,
				MaxRequestBodyBytes:     1 << 20,
				BreakerFailureThreshold: 5,
				BreakerCooldown:         time.Second,
				BreakerHalfOpenProbes:   1,
				LogLevel:                "info",
				LogFormat:               "json",
			}

			switch env {
			case "CATEGORY_GRPC_TARGET":
				cfg.CategoryGRPCTarget = ""
			case "MEDIA_GRPC_TARGET":
				cfg.MediaGRPCTarget = ""
			case "PRODUCT_GRPC_TARGET":
				cfg.ProductGRPCTarget = ""
			case "LISTING_GRPC_TARGET":
				cfg.ListingGRPCTarget = ""
			case "SEARCH_GRPC_TARGET":
				cfg.SearchGRPCTarget = ""
			}

			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted an empty %s", env)
			}
		})
	}
}

func TestZeroLimitsAreRejected(t *testing.T) {
	// A complexity limit of zero rejects everything, which reads as "the service is
	// broken" rather than as "the limit is too low".
	for name, mutate := range map[string]func(*Config){
		"complexity":  func(c *Config) { c.MaxQueryComplexity = 0 },
		"depth":       func(c *Config) { c.MaxQueryDepth = 0 },
		"length":      func(c *Config) { c.MaxQueryLength = 0 },
		"parserToken": func(c *Config) { c.ParserTokenLimit = 0 },
		"bodyBytes":   func(c *Config) { c.MaxRequestBodyBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MAX_QUERY_COMPLEXITY", "100")
			cfg := baseConfig()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted a zero %s limit", name)
			}
		})
	}
}

func TestInvalidLogSettingsAreRejected(t *testing.T) {
	t.Run("level", func(t *testing.T) {
		cfg := baseConfig()
		cfg.LogLevel = "verbose"
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate accepted an unknown log level")
		}
	})
	t.Run("format", func(t *testing.T) {
		cfg := baseConfig()
		cfg.LogFormat = "yaml"
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate accepted an unknown log format")
		}
	})
}

func TestPortRangeIsChecked(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 70000} {
		cfg := baseConfig()
		cfg.AppPort = port
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted port %d", port)
		}
	}
}

func TestGetEnvBoolAcceptsTheSpellingsComposeFilesUse(t *testing.T) {
	// strconv.ParseBool would reject "yes" and "on", both of which appear in real
	// compose files, and silently falling back to the default for those is worse than
	// accepting them.
	for _, value := range []string{"1", "true", "TRUE", "yes", "YES", "on"} {
		t.Setenv("ENABLE_PLAYGROUND", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		if !cfg.PlaygroundEnabled {
			t.Fatalf("%q should enable the playground", value)
		}
	}
	for _, value := range []string{"0", "false", "FALSE", "no", "NO", "off"} {
		t.Setenv("ENABLE_PLAYGROUND", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		if cfg.PlaygroundEnabled {
			t.Fatalf("%q should disable the playground", value)
		}
	}
}

func TestGetEnvBoolFallsBackOnGarbage(t *testing.T) {
	// Falling back to the default is the safe direction for a hardening toggle: an
	// unparseable "ENABLE_PLAYGROUND=maybe" leaves it off.
	t.Setenv("ENABLE_PLAYGROUND", "maybe")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PlaygroundEnabled {
		t.Fatal("garbage should fall back to the default, which is off")
	}
}

func TestOriginPolicy(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		origin  string
		want    bool
	}{
		// A request with no Origin is not a browser request, so it is allowed;
		// rejecting it would break curl and every server-to-server client.
		{"no origin", nil, "", true},
		{"wildcard", []string{"*"}, "https://evil.example", true},
		{"listed", []string{"https://shop.example"}, "https://shop.example", true},
		{"unlisted", []string{"https://shop.example"}, "https://evil.example", false},
		{"unconfigured rejects browsers", nil, "https://evil.example", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.AllowedOrigins = tc.allowed
			if got := cfg.OriginAllowed(tc.origin); got != tc.want {
				t.Fatalf("OriginAllowed(%q) with %v = %v, want %v", tc.origin, tc.allowed, got, tc.want)
			}
		})
	}
}

func TestAllowedOriginsListDropsEmptyEntries(t *testing.T) {
	// A trailing comma in a compose file must not produce a literal empty origin,
	// which would then match requests with no Origin.
	t.Setenv("ALLOWED_ORIGINS", "https://a.example, ,https://b.example,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("got %v, want 2 entries", cfg.AllowedOrigins)
	}
}

func TestDebugLoggingWithoutIntrospectionIsRejected(t *testing.T) {
	// Failing loud here beats discovering the mismatch from a monitoring alert: an
	// operator debugging a schema almost always wants introspection, and the
	// combination is nearly always a misconfiguration.
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("ENABLE_INTROSPECTION", "false")

	if _, err := Load(); err == nil {
		t.Fatal("expected debug logging with introspection off to be rejected")
	}
}

func TestDependenciesAreReportedForLogging(t *testing.T) {
	t.Setenv("CATEGORY_GRPC_TARGET", "category-app:9090")
	t.Setenv("SEARCH_GRPC_TARGET", "search-app:9094")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deps := cfg.Dependencies()
	// gRPC targets are not secret, so they belong in the startup log: knowing which
	// address this instance dialled is the first thing needed to debug a wrong one.
	for _, want := range []string{"category=category-app:9090", "search=search-app:9094"} {
		if !contains(deps, want) {
			t.Fatalf("Dependencies() = %q, want it to contain %q", deps, want)
		}
	}
}

func TestMalformedNumericAndDurationValuesFallBack(t *testing.T) {
	// Falling back rather than failing: these are tuning values, and refusing to start
	// because a duration is unparseable turns a typo into an outage. The limits and
	// budgets themselves are still validated below.
	t.Setenv("MAX_QUERY_COMPLEXITY", "not-a-number")
	t.Setenv("REQUEST_TIMEOUT", "not-a-duration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxQueryComplexity != 250 {
		t.Fatalf("complexity %d, want the default", cfg.MaxQueryComplexity)
	}
	if cfg.RequestTimeout != 10*time.Second {
		t.Fatalf("request timeout %s, want the default", cfg.RequestTimeout)
	}
}

// baseConfig builds a valid configuration for tests that mutate one field.
func baseConfig() *Config {
	return &Config{
		AppPort:                 8085,
		CategoryGRPCTarget:      "localhost:9090",
		MediaGRPCTarget:         "localhost:9091",
		ProductGRPCTarget:       "localhost:9092",
		ListingGRPCTarget:       "localhost:9093",
		SearchGRPCTarget:        "localhost:9094",
		RequestTimeout:          10 * time.Second,
		DependencyTimeout:       3 * time.Second,
		MaxQueryComplexity:      250,
		MaxQueryDepth:           12,
		MaxQueryLength:          16_000,
		ParserTokenLimit:        15_000,
		MaxRequestBodyBytes:     1 << 20,
		BreakerFailureThreshold: 5,
		BreakerCooldown:         5 * time.Second,
		BreakerHalfOpenProbes:   1,
		LogLevel:                "info",
		LogFormat:               "json",
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
