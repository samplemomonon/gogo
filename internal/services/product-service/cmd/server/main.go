// Command server runs the product command REST and gRPC APIs, and the outbox
// relay that publishes the resulting domain events to Kafka.
//
// This is the write side of the product domain. Reading products is
// product-listing-service's job: it consumes the events published here and
// maintains its own read model, so reads never touch this service's database.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"product-service/gen/productpb"
	"product-service/internal/config"
	"product-service/internal/di"
	"product-service/internal/event"
	"product-service/internal/messaging"
	"product-service/internal/outbox"
	"product-service/internal/version"
)

func main() {
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
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage: product-service [flags]

Serves the product command REST and gRPC APIs, and relays the domain events of
every committed change to Kafka. Category and media references are validated
against category-service and media-service over gRPC; the connections are lazy,
so this service starts even when its dependencies are still coming up.

Reading products is product-listing-service's job, reached on port 8083. This
service serves no GET routes on purpose.

Configuration comes from the environment; see the README for the full list of
variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints (writes only):
  POST   /api/v1/products                     create a product
  PUT    /api/v1/products/{id}                update a product
  DELETE /api/v1/products/{id}                delete a product
  POST   /api/v1/products/{id}/stock          adjust stock by a delta
  POST   /api/v1/products/{id}/variants       create a variant
  PUT    /api/v1/products/{id}/variants/{vid} update a variant
  DELETE /api/v1/products/{id}/variants/{vid} delete a variant
  GET    /health                              liveness probe
  GET    /readyz                              readiness probe (checks the database)
  GET    /version                             build information

gRPC (internal, port 9092 by default):
  productpb.ProductService   create, update, delete

Events (published to `+event.Topic+`):
  product.v1.product.created       product.v1.variant.created
  product.v1.product.updated       product.v1.variant.updated
  product.v1.product.deleted       product.v1.variant.deleted
  product.v1.product.stock_adjusted
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
		"event_topic", event.Topic,
		"addr", cfg.Addr(),
		"grpc_addr", cfg.GRPCAddr(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, cleanup, err := di.InitializeApp(cfg, log)
	if err != nil {
		return err
	}
	defer cleanup()

	// The relay gets a context of its own rather than the signal one, so it can
	// finish its current batch during the drain window instead of being cut off
	// by the signal that started the shutdown.
	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	relayDone := make(chan error, 1)

	outboxStore := outbox.NewStore(app.DB)

	// Create the outbox relay producer (old-style that publishes event.Record)
	relayProducer, err := messaging.NewProducer(messaging.ProducerConfig{
		Brokers:        cfg.KafkaBrokers,
		ClientID:       cfg.KafkaClientID,
		PublishTimeout: cfg.KafkaPublishTimeout,
	}, log)
	if err != nil {
		return err
	}
	relay := outbox.NewRelay(outboxStore, relayProducer, outbox.RelayConfig{
		BatchSize:    cfg.OutboxBatchSize,
		PollInterval: cfg.OutboxPollInterval,
		Lease:        cfg.OutboxLease,
		MaxAttempts:  cfg.OutboxMaxAttempts,
		DSN:          cfg.DSN(),
		IsPermanent:  messaging.IsPermanent,
	}, log)
	go func() { relayDone <- relay.Run(relayCtx) }()

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           buildHandler(app, log),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	grpcServer := buildGRPCServer(app)

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

	var runErr error
	select {
	case err := <-httpErr:
		runErr = err
	case err := <-grpcErr:
		runErr = err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := stopGRPC(shutdownCtx, grpcServer, log); err != nil {
		log.Error("graceful grpc shutdown failed, forcing", "error", err)
		grpcServer.Stop()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	stopRelay()
	select {
	case err := <-relayDone:
		if err != nil {
			log.Error("outbox relay stopped with an error", "error", err)
		}
	case <-shutdownCtx.Done():
		log.Warn("outbox relay did not finish within the shutdown window, "+
			"unpublished events remain in the outbox", "timeout", cfg.ShutdownTimeout)
	}

	closeErr := relayProducer.Close(shutdownCtx)
	if closeErr != nil {
		log.Error("closing relay producer, buffered events will be republished from the outbox",
			"error", closeErr)
		if runErr == nil {
			runErr = closeErr
		} else {
			runErr = errors.Join(runErr, closeErr)
		}
	}

	kafkaCloseErr := app.KafkaProducer.Close(shutdownCtx)
	if kafkaCloseErr != nil {
		if runErr == nil {
			runErr = kafkaCloseErr
		} else {
			runErr = errors.Join(runErr, kafkaCloseErr)
		}
	}

	if err := app.CategoryClient.Close(); err != nil {
		log.Error("close category client", "error", err)
	}
	if err := app.MediaClient.Close(); err != nil {
		log.Error("close media client", "error", err)
	}
	if err := app.DB.Close(); err != nil {
		log.Error("close database", "error", err)
	}

	if runErr != nil {
		log.Info("server stopped with errors")
		return runErr
	}
	log.Info("server stopped gracefully")
	return nil
}

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

func buildGRPCServer(app *di.App) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(16<<20),
		grpc.MaxSendMsgSize(16<<20),
		grpc.UnaryInterceptor(recoveryInterceptor(app.Logger)),
		grpc.StreamInterceptor(streamRecoveryInterceptor(app.Logger)),
	)

	productpb.RegisterProductServiceServer(server, app.GRPCServer)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(server)
	return server
}

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

func buildHandler(app *di.App, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	app.HealthHandler.RegisterRoutes(mux)
	app.ProductHandler.RegisterRoutes(mux)

	return recoveryMiddleware(log)(
		loggingMiddleware(log)(
			securityHeadersMiddleware(mux),
		),
	)
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func recoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		defer func() {
			if err := recover(); err != nil {
				log.Error("panic in grpc handler", "error", err)
			}
		}()
		return handler(ctx, req)
	}
}

func streamRecoveryInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		defer func() {
			if err := recover(); err != nil {
				log.Error("panic in grpc stream", "error", err)
			}
		}()
		return handler(srv, ss)
	}
}

func recoveryMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error("panic in handler", "error", err)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func loggingMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Info("request", "method", r.Method, "path", r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
