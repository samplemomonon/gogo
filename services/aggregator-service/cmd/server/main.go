// Command server runs the GraphQL aggregator: the composition layer in front of
// the five platform services.
//
// It owns no data and no database. Everything it answers is read from another
// service at request time, which is the property that keeps it from becoming a
// sixth place where the catalogue has to be kept in sync.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aggregator-service/graph"
	"aggregator-service/graph/generated"
	gqlmodel "aggregator-service/graph/model"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/config"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/grpcclient"
	"aggregator-service/internal/handler"
	"aggregator-service/internal/projection"
	"aggregator-service/internal/retry"
	"aggregator-service/internal/serviceauth"
	"aggregator-service/internal/telemetry"
	"aggregator-service/internal/version"
)

func main() {
	// A -version flag lets container health checks and operators confirm which
	// build is running without starting the server.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "-version", "--version":
			fmt.Println(version.String())
			return
		case "-help", "--help", "-h":
			usage(os.Stdout)
			return
		}
	}

	if err := run(); err != nil {
		// The logger may not exist yet, so report to stderr directly.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage: aggregator-service [flags]

Serves the GraphQL composition layer over the five platform services:

  category-service         categories and the hierarchy
  media-service            media metadata and presigned object URLs
  product-service          product writes (command side only)
  product-listing-service  browse reads (listing read model)
  product-search-service   free-text search (search read model)

This service owns no data. Reads come from the two read models, which are
eventually consistent with the write side, so a product created a moment ago may
not be readable yet. WriteProductResult reports whether a projection has caught up
with a write, and projectionHealth reports how far behind each read model is.

Configuration comes from the environment; see the README for the full list of
variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints:
  POST   /query                    GraphQL (GET accepted for read-only queries)
  GET    /playground               interactive schema browser, when enabled
  GET    /health                   liveness probe, touches no dependency
  GET    /readyz                   readiness probe, checks every dependency
  GET    /version                  build information

Hardening applied to every query:
  parser token limit, query length limit, body size limit
  query complexity limit, query depth limit
  introspection toggle, Automatic Persisted Queries
`)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting",
		"build", version.String(),
		"dependencies", cfg.Dependencies(),
		"addr", cfg.Addr(),
		"introspection", cfg.EnableIntrospection,
		"playground", cfg.PlaygroundEnabled,
		"max_complexity", cfg.MaxQueryComplexity,
		"max_depth", cfg.MaxQueryDepth,
		"request_timeout", cfg.RequestTimeout.String(),
	)

	// signal.NotifyContext cancels ctx on SIGINT/SIGTERM, which drives the graceful
	// shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Tracing is initialised before anything that makes a downstream call, so the
	// exporter's own startup work is not attributed to the first request. A failure here
	// is fatal: it means a misconfigured collector, and continuing would produce a
	// service that silently exports nothing while claiming to be traced.
	tracing, err := telemetry.New(telemetry.Config{
		Enabled:        cfg.TracingEnabled,
		ServiceName:    cfg.TracingServiceName,
		ServiceVersion: cfg.TracingServiceVersion,
		Environment:    cfg.TracingEnvironment,
		Endpoint:       cfg.TracingEndpoint,
		Insecure:       cfg.TracingInsecure,
		Headers:        cfg.TracingHeaders,
		SampleRatio:    cfg.TracingSampleRatio,
	})
	if err != nil {
		return fmt.Errorf("initialise tracing: %w", err)
	}
	// Flushes any buffered spans on the way out. Without it, spans in the batch queue are
	// lost, which during an incident is exactly the set most wanted.
	defer func() {
		// A fresh context: the signal context is already cancelled by this point, and a
		// flush against a cancelled context would drop the very spans it is trying to
		// save.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), telemetry.ShutdownTimeout)
		defer cancel()
		if err := telemetry.Shutdown(shutdownCtx, tracing); err != nil {
			log.Error("flushing traces", "error", err)
		}
	}()

	if cfg.SampleEverythingInProduction() {
		log.Warn("tracing is fully sampled outside development; this service emits several spans per request, so the export volume is the request volume multiplied")
	}

	tracer := tracing.Tracer()
	observer := telemetry.NewObserver(tracer)

	// Dialing is lazy, so this returns even if a dependency is still starting. The
	// readiness probe is where an unreachable dependency surfaces, and it can say
	// which one.
	// Declared before it is assigned because the OnRetry hook refers to it. A single
	// statement cannot do both: the variable is not in scope until the end of the
	// declaration, and the hook is inside it.
	var readPolicy retry.Policy
	readPolicy = retry.New(retry.Options{
		MaxAttempts: cfg.ReadRetryAttempts,
		BaseDelay:   cfg.ReadRetryBase,
		MaxDelay:    cfg.ReadRetryMax,
		OnRetry: func(attempt int, delay time.Duration, err error) {
			// Retries are otherwise invisible. A dependency that needs three attempts on
			// every request is a problem long before it fails outright, and this is the
			// only signal that says so.
			log.Warn("retrying downstream call",
				"attempt", attempt,
				"max_attempts", readPolicy.MaxAttempts(),
				"delay_ms", delay.Milliseconds(),
				"error", err.Error(),
			)
		},
	})

	// Writes get one attempt. product-service has no idempotency key on
	// CreateProduct, so a repeated call is a second product, and the cost of that
	// duplicate is far higher than the cost of a failed write the client retries
	// deliberately.
	writePolicy := retry.Sensitive()

	// One credential for every downstream call. Config refuses to start without
	// it, so this is always present in a running service.
	downstream := serviceauth.NewOutgoing(cfg.DownstreamServiceToken)

	clients, err := grpcclient.New(grpcclient.Targets{
		Credential:   downstream,
		Category:     cfg.CategoryGRPCTarget,
		Media:        cfg.MediaGRPCTarget,
		Product:      cfg.ProductGRPCTarget,
		Listing:      cfg.ListingGRPCTarget,
		Search:       cfg.SearchGRPCTarget,
		Timeout:      cfg.DependencyTimeout,
		Observer:     observer,
		StatsHandler: telemetry.GRPCClientStatsHandler(),
		ReadPolicy:   readPolicy,
		WritePolicy:  writePolicy,
		Breaker: breaker.Options{
			FailureThreshold: cfg.BreakerFailureThreshold,
			Cooldown:         cfg.BreakerCooldown,
			HalfOpenProbes:   cfg.BreakerHalfOpenProbes,
			// A probe is bounded below the per-attempt timeout, so a half-open breaker
			// notices a dependency that accepts a connection and then stalls.
			ProbeTimeout: cfg.DependencyTimeout / 2,
		},
		OnBreakerStateChange: func(name string, from, to breaker.State) {
			// A breaker flapping is an incident signal in its own right, and it is
			// invisible without this line: the requests it refuses are logged as
			// failures by the client that made them, which looks like the dependency
			// being slow rather than this service shedding load deliberately.
			log.Warn("circuit breaker changed state",
				"dependency", name,
				"from", from.String(),
				"to", to.String(),
			)
		},
	})
	if err != nil {
		return fmt.Errorf("dial downstream services: %w", err)
	}
	defer func() {
		if err := clients.Close(); err != nil {
			log.Error("close grpc connections", "error", err)
		}
	}()

	projectionReporter := projection.New(projection.Options{
		Credential:  downstream,
		ListingBase: cfg.ListingHTTPBase,
		SearchBase:  cfg.SearchHTTPBase,
		Timeout:     cfg.DependencyTimeout,
		// The status calls are the aggregator's only outbound HTTP, so this is where
		// they become spans. Without it a slow or failing status endpoint would be
		// invisible in a trace even though it sits on the path of every write
		// response.
		Transport: telemetry.HTTPTransport(http.DefaultTransport),
	})

	root := &graph.Resolver{
		CatalogStore:    grpcclient.NewCatalog(clients.Listing),
		SearchStore:     grpcclient.NewSearch(clients.Search),
		CategoryStore:   grpcclient.NewCategories(clients.Category),
		MediaStore:      grpcclient.NewMedia(clients.Media),
		ProductStore:    grpcclient.NewProducts(clients.Product),
		ProjectionStore: projectionReporter,
	}
	// Attached after construction because the observer is wiring rather than a
	// collaborator: it is set once at startup and never swapped.
	root.WithObserver(telemetry.NewResolverObserver(observer))

	exec := generated.NewExecutableSchema(generated.Config{
		Resolvers:  root,
		Complexity: newComplexityRoot(),
	})

	server := &handler.Server{
		GraphQL:    handler.NewGraphQL(cfg, exec, log),
		Playground: handler.PlaygroundHandler(cfg),
		ReadyCheck: readinessCheck(clients, projectionReporter, cfg.DependencyTimeout),
		Config:     cfg,
		Log:        log,
	}

	// The HTTP handler is wrapped rather than built into server.Handler so the span is
	// created before the recovery and budget middleware. The order matters: a panic or
	// an exhausted budget then appears inside the span rather than outside it, which is
	// what makes "requests that blew their budget" a query rather than an inference.
	tracedHandler := telemetry.HTTPHandler(graph.RequestCacheMiddleware(server.Handler()))

	httpServer := &http.Server{
		Addr:    cfg.Addr(),
		Handler: tracedHandler,
		// ReadHeaderTimeout is the important one: it bounds how long a client may
		// take to send headers, which is the slowloris window. Read and write
		// timeouts exceed the request budget, because a query that runs to its
		// budget still has to be serialised and flushed.
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// ListenAndServe blocks, so it runs on its own goroutine and reports its
	// outcome through a channel.
	httpErr := serve("http", func() error {
		log.Info("listening", "addr", cfg.Addr(), "proto", "http")
		return httpServer.ListenAndServe()
	})

	var runErr error
	select {
	case err := <-httpErr:
		// The listener failed before any signal arrived.
		runErr = err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.ShutdownTimeout)
	}

	// Detach the shutdown from the already-cancelled signal context, so the drain
	// window is not cut short by the signal that triggered it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Stop taking requests before closing the connections they use: a resolver
	// mid-flight would otherwise find its client closed underneath it.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("server stopped gracefully")
	return runErr
}

// newComplexityRoot weights query cost.
//
// gqlgen's default model charges one per field per level, which makes a page of 20
// products cost about the same as a single product. That is wrong for a cost-based
// limit: the expensive request is the one that returns many objects, so it is
// exactly the one that must be budgeted. The list fields below multiply their child
// cost by the caller's page size, which puts a real page at a real price and makes
// the configured limit mean something.
//
// The multiplier is the argument count rather than the page size from the filter,
// because a query that requests 1000 items is priced at 1000 whether or not the
// browse service will return that many. Pricing on what was asked for is the
// conservative direction.
func newComplexityRoot() generated.ComplexityRoot {
	var complexity generated.ComplexityRoot

	// scale multiplies a child cost by a requested page size, saturating rather than
	// wrapping. An overflow here would produce a tiny cost for a very large query,
	// which is exactly the bypass a limit exists to prevent.
	//
	// The floor of 1 per item is what stops a second bypass. With scalars priced at
	// zero, a list field whose selection is entirely scalars has a child cost of 0, and
	// `0 * 60` is 0 — so an alias fan-out over `products { total }` would price at
	// nothing no matter how many times it was repeated. The page is still 60 rows off
	// the read model either way, so each item costs at least one.
	scale := func(childComplexity, count int) int {
		if count <= 0 {
			return childComplexity
		}
		perItem := childComplexity
		if perItem < 1 {
			perItem = 1
		}
		if count > maxInt/perItem {
			return maxInt
		}
		return perItem * count
	}

	// requestedLimit reads a page size out of an argument, treating an absent or
	// non-positive value as the service default. Pricing on what was asked for
	// rather than what will be returned is the conservative direction: a query
	// asking for 1000 items is charged as if it returned 1000, even if the browse
	// service will cap it at 100.
	requestedLimit := func(limit *int) int {
		if limit == nil || *limit <= 0 {
			return defaultLimit
		}
		return *limit
	}

	// The filter-based list fields take their page size from the filter argument.
	// Reading it here rather than using a fixed multiplier is what makes the cost
	// track the request: without it a client could ask for a thousand products and
	// be charged for twenty.
	complexity.Query.Products = func(childComplexity int, filter *gqlmodel.ProductFilter) int {
		if filter == nil || filter.Limit == nil {
			return scale(childComplexity, defaultLimit)
		}
		return scale(childComplexity, requestedLimit(filter.Limit))
	}

	complexity.Query.SearchProducts = func(childComplexity int, filter gqlmodel.SearchFilter) int {
		if filter.Limit == nil {
			return scale(childComplexity, defaultLimit)
		}
		return scale(childComplexity, requestedLimit(filter.Limit))
	}

	complexity.Query.Categories = func(childComplexity int, limit *int, offset *int) int {
		return scale(childComplexity, requestedLimit(limit))
	}

	complexity.Query.MediaList = func(childComplexity int, ownerID string, status *model.MediaStatus, limit *int, offset *int) int {
		return scale(childComplexity, requestedLimit(limit))
	}

	// CategoryTree and the recursive Category fields deliberately keep gqlgen's
	// default cost of one per field per level.
	//
	// A multiplier here would compound rather than apply once. `children` is
	// self-referential, so pricing it at 20x means a query nesting it 14 levels deep
	// costs 20^14 — which is why an earlier version of this file priced the recursive
	// fields and the complexity limit fired at ~1.6e18 on a query the depth limit
	// should have been the one to reject. Multipliers are only safe on fields that
	// cannot nest: the paginated roots above, where the page size is read from the
	// caller's argument and the field appears once.
	//
	// Recursion is bounded by MAX_QUERY_DEPTH instead, which is the limit that actually
	// describes this shape: the cost of a nested selection is linear in its depth and
	// independent of how many categories the tree holds.
	return complexity
}

// defaultLimit mirrors the resolver's default page size, used when a complexity
// function is asked to price a query that did not specify one. It lives here rather
// than in the graph package because complexity pricing runs during validation,
// before any resolver has applied its defaults.
const defaultLimit = 20

// maxInt bounds complexity arithmetic. Go's int is 64-bit here, but the limit is
// platform-dependent and an overflow would silently invert the check.
const maxInt = int(^uint(0) >> 1)

// serve runs an accept loop on its own goroutine and returns a channel carrying its
// outcome. A clean shutdown reports nil.
func serve(name string, run func() error) <-chan error {
	done := make(chan error, 1)
	go func() {
		if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- fmt.Errorf("%s server: %w", name, err)
			return
		}
		done <- nil
	}()
	return done
}

// readinessCheck probes every dependency.
//
// The aggregator is a composition layer, so every dependency is load-bearing for at
// least part of the schema. Reporting ready with one unreachable would send traffic
// guaranteed to fail part of its response.
//
// The checks run concurrently because they are independent, and sequential probes
// would make the probe's duration the sum of all of them: long enough to trip the
// probe's own timeout during exactly the partial outage it exists to detect.
func readinessCheck(clients *grpcclient.Clients, reporter *projection.Reporter, timeout time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		type check struct {
			name string
			run  func(context.Context) error
		}
		checks := []check{
			{"product-listing-service", func(context.Context) error {
				_, err := clients.Listing.ListProducts(ctx, grpcclient.ProductFilter{Limit: 1})
				return err
			}},
			{"product-search-service", func(context.Context) error {
				_, err := clients.Search.SearchProducts(ctx, grpcclient.SearchFilter{Query: "ready", Limit: 1})
				return err
			}},
			{"category-service", func(context.Context) error {
				_, _, err := clients.Category.ListCategories(ctx, 1, 0)
				return err
			}},
			{"media-service", func(context.Context) error {
				_, _, err := clients.Media.ListMedia(ctx, "", "", 1, 0)
				return err
			}},
			{"listing projection status", func(context.Context) error {
				_, err := reporter.ListingStatus(ctx)
				return err
			}},
			{"search projection status", func(context.Context) error {
				_, err := reporter.SearchStatus(ctx)
				return err
			}},
		}

		// Every check publishes exactly one result, success included. The reader
		// expects one message per check, so a silent success would deadlock the
		// probe on the channel instead of reporting ready.
		errs := make(chan error, len(checks))
		for _, c := range checks {
			go func(c check) {
				if err := c.run(ctx); err != nil {
					errs <- fmt.Errorf("%s: %w", c.name, err)
					return
				}
				errs <- nil
			}(c)
		}

		var failures []error
		for range checks {
			if err := <-errs; err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		// Load already validated the level, but never fail startup over logging.
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
