// Command server runs the product search REST and gRPC APIs, and the Kafka
// consumer that keeps its search index up to date.
//
// This is the search half of the product domain, split out of
// product-listing-service because searching and browsing are different workloads.
// It never talks to product-service at request time: it consumes the same events
// that service publishes and indexes them into its own database, so a search costs
// one indexed query here and nothing at all on the write path.
//
// The search implementation sits behind search.Engine. Today that is PostgreSQL
// with pg_trgm; replacing it with Elasticsearch or OpenSearch is a new
// implementation of that interface and a wiring change here.
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
	"product-search-service/internal/serviceauth"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"product-search-service/gen/searchpb"
	"product-search-service/internal/config"
	"product-search-service/internal/database"
	"product-search-service/internal/event"
	"product-search-service/internal/grpcsrv"
	"product-search-service/internal/handler"
	"product-search-service/internal/messaging"
	"product-search-service/internal/projection"
	"product-search-service/internal/repository"
	"product-search-service/internal/search"
	"product-search-service/internal/version"
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
	_, _ = fmt.Fprint(w, `Usage: product-search-service [flags]

Serves the product search REST and gRPC APIs from its own trigram-indexed read
model, and keeps that index current by consuming the domain events
product-service publishes.

This service holds no authoritative product data. A product created a moment ago
may not be searchable yet, because the event still has to travel through Kafka and
be indexed. GET /api/v1/search/status reports how far behind the index is.

Browsing the catalogue is not this service's job: a structured, filterable list
without a search term is served by product-listing-service on port 8083.

Configuration comes from the environment; see the README for the full list of
variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints (reads only):
  GET    /api/v1/search/products               search products by text
  GET    /api/v1/search/status                 index progress
  GET    /health                              liveness probe
  GET    /readyz                              readiness probe
  GET    /version                             build information

gRPC (internal, port 9094 by default):
  searchpb.ProductSearchService   search products

Consumed topic (set with KAFKA_TOPIC):
  `+event.Topic+`
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
		"database", cfg.RedactedDSN(),
		"dependencies", cfg.Dependencies(),
		"topic", cfg.KafkaTopic,
		"consumer_group", cfg.KafkaConsumerGroup,
		"consumer_enabled", cfg.ConsumerEnabled,
		"engine", "postgresql-pg_trgm",
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

	// Installs pg_trgm along with the schema. It is a trusted extension, so the
	// owner of this database is enough and no superuser is involved.
	if err := database.Migrate(ctx, db, log); err != nil {
		return err
	}

	// The engine is the replaceable half. Everything above it - the service
	// layer, the handlers, the response shape - is engine agnostic.
	engine := repository.NewEngine(db)
	svc := search.NewService(engine)
	projectorRepo := repository.NewProjector(db, engine)
	projector := projection.NewProjector(projectorRepo, log)

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
		// The search API is still served, so this is a warning rather than a fatal
		// error: an operator may be running it deliberately against an index built
		// by hand.
		log.Warn("consumer disabled, the search index will not be updated from Kafka")
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           buildHandler(svc, db, engine, projectorRepo, log, cfg.InboundServiceTokens),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	grpcServer := buildGRPCServer(svc, cfg.GRPCMaxMessageSize)

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
func buildGRPCServer(svc grpcsrv.ProductSearchService, maxMessageSize int) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		// A panic in a gRPC handler exits the process: grpc-go runs each RPC on its
		// own goroutine and does not recover. Without these interceptors any
		// reachable handler bug is an outage rather than a failed request.
		grpc.UnaryInterceptor(grpcsrv.UnaryRecovery(slog.Default())),
		grpc.StreamInterceptor(grpcsrv.StreamRecovery(slog.Default())),
	)

	searchpb.RegisterProductSearchServiceServer(server, grpcsrv.NewServer(svc, slog.Default()))

	// A standard health service lets load balancers probe gRPC directly. It
	// reports SERVING as soon as the process is up: the dependency checks live in
	// the HTTP readiness probe, which can actually describe what is broken.
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(server)
	return server
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
// probePaths are answered without a credential.
//
// They must be: an orchestrator that receives 401 from a liveness probe restarts
// the container, and restarting the process answering the probe turns a partial
// outage into a total one.
var probePaths = []string{"/health", "/healthz", "/readyz", "/version"}

// guardedHandler wraps mux with inbound authentication.
//
// Innermost in the chain, deliberately: logging runs before it so a refusal is
// recorded, and rate limiting runs before it so an unauthenticated caller is
// still throttled rather than being able to hammer the credential check.
//
// Extracted so the wiring is testable. A guard that only exists inline in the
// chain can be deleted without any test noticing, which is what happened in
// product-service before this was pulled out.
func guardedHandler(mux http.Handler, tokens []string) http.Handler {
	return serviceauth.NewTokenSet(tokens).HTTP(mux, probePaths)
}

func buildHandler(
	svc handler.ProductSearchService,
	db *sql.DB,
	engine *repository.Engine,
	projectorRepo *repository.Projector,
	log *slog.Logger,
	inboundTokens []string,
) http.Handler {
	mux := http.NewServeMux()

	// Readiness covers the database only. Kafka is deliberately excluded: a
	// consumer that has fallen behind still serves correct data, just older, and
	// taking the instance out of the load balancer would not make the index any
	// more current. The status endpoint reports the lag instead.
	handler.NewHealthHandler(func(ctx context.Context) error {
		return database.Ping(ctx, db)
	}, log).RegisterRoutes(mux)

	handler.NewSearchHandler(svc, log).RegisterRoutes(mux)
	handler.NewStatusHandler(statusReporter{
		documents: engine.Count,
		events: func(ctx context.Context) (int64, error) {
			stats, err := projectorRepo.Stats(ctx)
			return stats.AppliedEvents, err
		},
	}, log).RegisterRoutes(mux)

	return wrapChain(mux, log, inboundTokens)
}

// wrapChain is the middleware chain, taking the mux rather than the full
// dependency graph.
//
// Extracted so the chain is reachable from a test. buildHandler needs a wired
// service, database and projector, so a test could not otherwise reach the chain
// — and deleting the guard from it left the suite green until this existed.
func wrapChain(mux http.Handler, log *slog.Logger, inboundTokens []string) http.Handler {
	// Order matters: recover first so it also catches panics raised by the
	// logging and header middleware. Authentication is innermost, so a refusal is
	// still logged.
	return handler.Recovery(log)(
		handler.Logging(log)(
			handler.SecurityHeaders(guardedHandler(mux, inboundTokens)),
		),
	)
}

// statusReporter adapts the repository counters to what the status handler
// needs, so the handler does not depend on either repository.
type statusReporter struct {
	documents func(ctx context.Context) (int64, error)
	events    func(ctx context.Context) (int64, error)
}

func (r statusReporter) Documents(ctx context.Context) (int64, error) { return r.documents(ctx) }

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
