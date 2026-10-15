// Command server runs the product listing REST and gRPC APIs, and the Kafka
// consumer that keeps its read model up to date.
//
// This is the read side of the product domain. It never talks to product-service
// at request time: it consumes the events that service publishes and projects
// them into its own database, so browsing the catalogue costs one indexed query
// here and nothing at all on the write path.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/ruangbercerita182-tech/product-listing-service/gen/listingpb"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/cache"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/config"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/database"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/grpcsrv"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/handler"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/messaging"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/metrics"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/projection"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/ratelimit"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/repository"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/service"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/version"
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
	// Nothing useful to do if stdout is unwritable, e.g. a closed pipe.
	//
	// Fprintf, not Fprint: the topic defaults are named constants in the config
	// package so the loader and this text cannot drift. That only works if the
	// placeholders are actually formatted, which is the whole bug this replaced —
	// the previous text carried a template placeholder that no formatter ever
	// expanded, so -help printed the placeholder instead of the topic.
	_, _ = fmt.Fprintf(w, `Usage: product-listing-service [flags]

Serves the product read REST and gRPC APIs from its own read model, and keeps
that model current by consuming the domain events product-service publishes.

This service holds no authoritative product data. A product created a moment ago
may not be visible yet, because the event still has to travel through Kafka and be
projected. GET /api/v1/listing/status reports how far behind the projection is.

Writes go to product-service on port 8082. There are no mutating routes here.

Configuration comes from the environment; see the README for the full list of
variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints (reads only):
  GET    /api/v1/products                     list products with filters
  GET    /api/v1/products/{id}                fetch one product
  GET    /api/v1/products/{id}/variants       list variants
  GET    /api/v1/listing/status               projection progress
  GET    /health                              liveness probe
  GET    /readyz                              readiness probe
  GET    /version                             build information

gRPC (internal, port 9093 by default):
  listingpb.ProductListingService   get, list, list variants

Consumed topic (set with KAFKA_TOPIC, default %s):
  %s
Dead-letter topic (KAFKA_DEAD_LETTER_TOPIC, default %s):
  %s
`, config.DefaultKafkaTopic, config.DefaultKafkaTopic, config.DefaultKafkaDeadLetterTopic, config.DefaultKafkaDeadLetterTopic)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	// Initialize metrics
	m := metrics.New()

	// Initialize cache
	productCache := cache.New[*model.Product](cache.Options{
		MaxSize:    1000,
		DefaultTTL: 30 * time.Second,
	})

	// Initialize rate limiter
	rateLimiter := ratelimit.New(ratelimit.Options{
		Rate:            100,
		Burst:           150,
		CleanupInterval: time.Minute,
	})
	defer rateLimiter.Stop()

	log.Info("starting",
		"build", version.String(),
		"database", cfg.RedactedDSN(),
		"dependencies", cfg.Dependencies(),
		"topic", cfg.KafkaTopic,
		"consumer_group", cfg.KafkaConsumerGroup,
		"consumer_enabled", cfg.ConsumerEnabled,
		"addr", cfg.Addr(),
		"grpc_addr", cfg.GRPCAddr(),
	)

	// signal.NotifyContext cancels ctx on SIGINT/SIGTERM, which is what drives
	// both the database work and the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Connect(ctx, cfg.DSN(), database.Options{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnMaxLifetime,
	})
	if err != nil {
		return err
	}
	// Closing after Shutdown lets in-flight requests finish their queries.
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}()

	if err := database.Migrate(ctx, db, log); err != nil {
		return err
	}

	listingRepo := repository.NewListingRepository(db)
	projectorRepo := repository.NewProjector(db)

	// The service owns the cache; the projection is told to invalidate through it.
	// Wiring the same object to both is what makes a projected write visible to a
	// reader immediately instead of one TTL later.
	// The cache is counted here rather than inside the service, so the business
	// logic stays free of any Prometheus dependency and the instrumentation can be
	// added or removed without touching it.
	svc := service.NewProductListingService(listingRepo, log, newMetricsCache(productCache, m))
	projector := projection.NewProjector(projectorRepo, log, svc)

	var (
		consumer *messaging.Consumer
		producer *messaging.Producer
	)
	if cfg.ConsumerEnabled {
		// The dead-letter producer is only needed by the consumer, so it is built
		// with it rather than on every start.
		producer, err = messaging.NewProducer(cfg.KafkaBrokers, cfg.KafkaClientID, 10*time.Second, log)
		if err != nil {
			return err
		}
		consumer, err = messaging.NewConsumer(messaging.ConsumerConfig{
			Brokers:         cfg.KafkaBrokers,
			GroupID:         cfg.KafkaConsumerGroup,
			ClientID:        cfg.KafkaClientID,
			Topics:          []string{cfg.KafkaTopic},
			MaxPollRecords:  cfg.KafkaMaxPollRecords,
			PollTimeout:     cfg.KafkaPollTimeout,
			DeadLetterTopic: cfg.KafkaDeadLetterTopic,
			RetryBackoff:    cfg.KafkaRetryBackoff,
		}, projector, producer, log)
		if err != nil {
			return err
		}
	} else {
		// The read API is still served, so this is a warning rather than a fatal
		// error: an operator may be running it deliberately against a projection
		// built by hand.
		log.Warn("consumer disabled, the read model will not be updated from Kafka")
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           buildHandler(svc, db, listingRepo, projectorRepo, log, m, rateLimiter),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	grpcServer := buildGRPCServer(svc, cfg.GRPCMaxMessageSize, m)

	// ListenAndServe blocks, so each listener runs on its own goroutine and
	// reports its outcome through a channel.
	httpErr := serve("http", func() error {
		log.Info("listening", "addr", cfg.Addr(), "proto", "http")
		return httpServer.ListenAndServe()
	})
	grpcErr := serve("grpc", func() error {
		listener, err := net.Listen("tcp", cfg.GRPCAddr())
		if err != nil {
			return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr(), err)
		}
		log.Info("listening", "addr", cfg.GRPCAddr(), "proto", "grpc")
		return grpcServer.Serve(listener)
	})

	// The background workers get contexts of their own rather than the signal one,
	// so they can finish their current unit of work during the drain window.
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	// Written only by this function, closed by the consumer goroutine.
	var workerDone chan struct{}
	if consumer != nil {
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			if err := consumer.Run(workerCtx); err != nil {
				log.Error("consumer stopped with an error", "error", err)
			}
		}()
		go runPruner(workerCtx, projectorRepo, cfg, log)
	}

	var runErr error
	select {
	case err := <-httpErr:
		// A listener failed before any signal arrived.
		runErr = err
	case err := <-grpcErr:
		runErr = err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.ShutdownTimeout)
	}

	// Detach the shutdown from the already-cancelled signal context so the drain
	// window is not cut short by the signal that triggered it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Stop taking requests first, then stop consuming, so the service does not
	// claim to be down while still answering.
	if err := stopGRPC(shutdownCtx, grpcServer, log); err != nil {
		// A stuck gRPC stream must not block the process from exiting.
		log.Error("graceful grpc shutdown failed, forcing", "error", err)
		grpcServer.Stop()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	if workerDone != nil {
		// Close the consumer first so it leaves the group and stops fetching, then
		// wait for the run loop to notice. Uncommitted offsets are simply
		// re-read by the next instance, so an interrupted consumer is safe.
		consumer.Close()
		stopWorkers()
		select {
		case <-workerDone:
		case <-shutdownCtx.Done():
			log.Warn("consumer did not stop within the shutdown window, "+
				"uncommitted offsets will be re-read", "timeout", cfg.ShutdownTimeout)
		}
	} else {
		stopWorkers()
	}

	if producer != nil {
		if err := producer.Close(shutdownCtx); err != nil {
			log.Error("closing dead-letter producer", "error", err)
		}
	}

	log.Info("server stopped gracefully")
	return runErr
}

// runPruner deletes processed-event ids older than the retention window.
//
// The table exists to detect redeliveries, and those arrive within seconds of the
// original. Once a row is older than the window the broker will not replay that
// offset again, so keeping it only grows a table written on every event.
func runPruner(ctx context.Context, projector *repository.Projector, cfg *config.Config, log *slog.Logger) {
	ticker := time.NewTicker(cfg.PruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Bounded, so a large table cannot hold the connection indefinitely.
			pruneCtx, cancel := context.WithTimeout(ctx, time.Minute)
			removed, err := projector.PruneProcessedEvents(pruneCtx, cfg.ProcessedEventRetention)
			cancel()
			if err != nil {
				log.Warn("pruning processed events failed", "error", err)
				continue
			}
			if removed > 0 {
				log.Info("pruned processed events",
					"removed", removed, "retention", cfg.ProcessedEventRetention.String())
			}
		}
	}
}

// serve runs an accept loop on its own goroutine and returns a channel carrying
// its outcome. A clean shutdown reports nil.
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

// buildGRPCServer assembles the internal API. Reflection is registered so that
// grpcurl can explore the service without a copy of the .proto file.
func buildGRPCServer(svc grpcsrv.ProductListingService, maxMessageSize int, m *metrics.Metrics) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		// A panic in a gRPC handler exits the process: grpc-go runs each RPC on its
		// own goroutine and does not recover. Without these interceptors any
		// reachable handler bug is an outage rather than a failed request.
		grpc.ChainUnaryInterceptor(
			grpcsrv.UnaryRecovery(slog.Default()),
			metricsInterceptor(m),
		),
		grpc.StreamInterceptor(grpcsrv.StreamRecovery(slog.Default())),
	)

	listingpb.RegisterProductListingServiceServer(server, grpcsrv.NewServer(svc, slog.Default()))

	// A standard health service lets load balancers probe gRPC directly. It
	// reports SERVING as soon as the process is up: the dependency checks live in
	// the HTTP readiness probe, which can actually describe what is broken.
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(server)
	return server
}

// metricsInterceptor records metrics for gRPC requests.
func metricsInterceptor(m *metrics.Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		m.GRPCActiveRequests.Inc()
		defer m.GRPCActiveRequests.Dec()

		resp, err := handler(ctx, req)

		code := "OK"
		if err != nil {
			code = status.Code(err).String()
		}
		m.RecordGRPCRequest(info.FullMethod, code, time.Since(start))
		return resp, err
	}
}

// stopGRPC drains open streams, falling back to a hard stop when the deadline
// passes so a stuck peer cannot block shutdown.
func stopGRPC(ctx context.Context, server *grpc.Server, log *slog.Logger) error {
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// buildHandler wires the layers together and wraps them in middleware.
func buildHandler(
	svc handler.ProductListingService,
	db *sql.DB,
	listingRepo *repository.ListingRepository,
	projectorRepo *repository.Projector,
	log *slog.Logger,
	m *metrics.Metrics,
	rateLimiter *ratelimit.Limiter,
) http.Handler {
	mux := http.NewServeMux()

	// Readiness covers the database only. Kafka is deliberately excluded: a
	// consumer that has fallen behind still serves correct data, just older, and
	// taking the instance out of the load balancer would not make the read model
	// any more current. The status endpoint reports the lag instead.
	handler.NewHealthHandler(func(ctx context.Context) error {
		return database.Ping(ctx, db)
	}, log).RegisterRoutes(mux)

	handler.NewProductListingHandler(svc, log).RegisterRoutes(mux)
	handler.NewStatusHandler(statusReporter{
		products: listingRepo.Count,
		events: func(ctx context.Context) (int64, error) {
			stats, err := projectorRepo.Stats(ctx)
			return stats.AppliedEvents, err
		},
	}, log).RegisterRoutes(mux)

	// Metrics endpoint for Prometheus scraping
	mux.Handle("/metrics", m.Handler())

	return middlewareChain(mux, log, m, rateLimiter)
}

// middlewareChain wraps mux in the service's HTTP middleware.
//
// Extracted from buildHandler so the ordering can be tested. Ordering is not
// cosmetic here, and the reason is one property of net/http: a middleware only
// sees the context that its *callers* established, because `r.WithContext` hands
// the new request downward and nothing hands it back up.
//
// RequestID is therefore outermost. It has to be, or the id it puts in the
// context is invisible to everything logged above it — which is what the previous
// order did: RequestID wrapped the mux directly, so the access log could not carry
// the id that the X-Request-ID response header was handing the client.
//
// Recovery sits directly inside it, so a panic report can identify the request.
// It is still outside Logging, so a panic raised while writing the access log is
// still turned into a 500. The only panic this chain cannot catch is one raised by
// RequestID itself, which is a header write and a hex encode.
//
//	middlewareChain
//	└── RequestID       establishes the id; everything below can read it
//	    └── Recovery    reports the id it can now see
//	        └── Logging logs the id
//	            └── SecurityHeaders
//	                └── metrics  (labels by route, not by concrete path)
//	                    └── rateLimiter
//	                        └── mux
func middlewareChain(mux http.Handler, log *slog.Logger, m *metrics.Metrics, limiter *ratelimit.Limiter) http.Handler {
	return handler.RequestID(
		handler.Recovery(log)(
			handler.Logging(log)(
				handler.SecurityHeaders(
					metricsMiddleware(m)(
						limiter.Middleware(mux),
					),
				),
			),
		),
	)
}

// metricsMiddleware records metrics for HTTP requests.
func metricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			m.HTTPActiveRequests.Inc()
			defer m.HTTPActiveRequests.Dec()

			// Wrap the response writer to capture status and size
			recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			// A 429 is the rate limiter acting, and it is invisible in the request
			// count otherwise: the request never reached a handler, so the only
			// signal it left was a status code nobody was watching for it.
			if recorder.status == http.StatusTooManyRequests {
				m.RecordRateLimited()
			}

			duration := time.Since(start)
			m.RecordHTTPRequest(r.Method, routeLabel(r.URL.Path), recorder.status, duration, r.ContentLength, int64(recorder.bytes))
		})
	}
}

// routeLabel collapses a concrete request path back to its route pattern before it
// is used as a Prometheus label.
//
// RecordHTTPRequest turns this into a label value, and the previous code passed
// r.URL.Path, which contains product ids: every id a client ever asked for minted
// a new time series. A busy catalogue exhausts a Prometheus server's memory and
// makes the metric unqueryable long before the crash, and nothing in the code
// reports it because each individual increment looks fine.
//
// Replacing the numeric segments with a placeholder bounds the label set to the
// number of routes. The id is not lost: it is on the request id in the access log,
// and it is a column in whatever table answered the query.
func routeLabel(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if _, err := strconv.ParseInt(segment, 10, 64); err == nil {
			segments[i] = "{id}"
		}
	}
	return strings.Join(segments, "/")
}

// responseRecorder wraps http.ResponseWriter to capture status and bytes written.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// statusReporter adapts the repository counters to what the status handler
// needs, so the handler does not depend on either repository.
type statusReporter struct {
	products func(ctx context.Context) (int64, error)
	events   func(ctx context.Context) (int64, error)
}

func (r statusReporter) Products(ctx context.Context) (int64, error) { return r.products(ctx) }

func (r statusReporter) AppliedEvents(ctx context.Context) (int64, error) { return r.events(ctx) }

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
