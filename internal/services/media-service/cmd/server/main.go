// Command server runs the media REST and gRPC APIs.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"media-service/internal/serviceauth"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"media-service/gen/mediapb"
	"media-service/internal/config"
	"media-service/internal/database"
	"media-service/internal/grpcsrv"
	"media-service/internal/handler"
	"media-service/internal/repository"
	"media-service/internal/service"
	"media-service/internal/storage"
	"media-service/internal/version"
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
	_, _ = fmt.Fprint(w, `Usage: media-service [flags]

Serves the media REST and gRPC APIs. Metadata lives in PostgreSQL and object
bytes live in an S3-compatible store; clients upload and download directly
through presigned URLs. Configuration comes from the environment; see the README
for the full list of variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints:
  GET    /health                              liveness probe
  GET    /readyz                              readiness probe (database + object store)
  GET    /version                             build information
  POST   /api/v1/media                        register media, returns an upload URL
  GET    /api/v1/media                        list media for an owner
  GET    /api/v1/media/{id}                   fetch one media record
  DELETE /api/v1/media/{id}                   delete the object and its metadata
  GET    /api/v1/media/{id}/upload-url        reissue an upload URL
  POST   /api/v1/media/{id}/complete          verify the upload and mark it ready
  GET    /api/v1/media/{id}/download-url      presigned download URL

gRPC (internal, port 9091 by default):
  mediapb.MediaService   register, fetch, list, complete, download, delete
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
		"object_store", cfg.RedactedMinIO(),
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

	store, err := storage.NewS3Store(storage.Options{
		Endpoint:  cfg.MinIOEndpoint,
		AccessKey: cfg.MinIOAccessKey,
		SecretKey: cfg.MinIOSecretKey,
		Bucket:    cfg.MinIOBucket,
		Region:    cfg.MinIORegion,
		UseSSL:    cfg.MinIOUseSSL,
	})
	if err != nil {
		return err
	}
	// The store is usually still warming up when this process starts, so a brief
	// window of unavailability is retried rather than turned into a crash loop.
	if err := waitForBucket(ctx, store, cfg.MinIOEnsureBucket, log); err != nil {
		return err
	}

	repo := repository.NewMediaRepository(db)
	svc := service.NewMediaService(
		repo, store,
		cfg.MinIOPresignUploadTTL, cfg.MinIOPresignDownloadTTL,
		log,
	)

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           buildHandler(svc, store, db, log, cfg.InboundServiceTokens),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	grpcServer, err := buildGRPCServer(svc, cfg.GRPCMaxMessageSize)
	if err != nil {
		return err
	}

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

	if err := stopGRPC(shutdownCtx, grpcServer, log); err != nil {
		// A stuck gRPC stream must not block the process from exiting.
		log.Error("graceful grpc shutdown failed, forcing", "error", err)
		grpcServer.Stop()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped gracefully")
	return runErr
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
func buildGRPCServer(svc grpcsrv.MediaService, maxMessageSize int) (*grpc.Server, error) {
	// The limit bounds a single inbound message. Presigned URLs are long strings,
	// but nothing legitimate approaches the default ceiling.
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		// A panic in a gRPC handler exits the process: grpc-go runs each RPC on its
		// own goroutine and does not recover. Without these interceptors any
		// reachable handler bug is an outage rather than a failed request.
		grpc.UnaryInterceptor(grpcsrv.UnaryRecovery(slog.Default())),
		grpc.StreamInterceptor(grpcsrv.StreamRecovery(slog.Default())),
	)

	mediapb.RegisterMediaServiceServer(server, grpcsrv.NewServer(svc, slog.Default()))

	// A standard health service lets load balancers probe gRPC directly. It
	// reports SERVING as soon as the process is up: the dependency checks live in
	// the HTTP readiness probe, which can actually describe what is broken.
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(server)
	return server, nil
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

// waitForBucket retries EnsureBucket until it succeeds or the context is done.
// The store is a hard dependency, but a container that has just started is
// usually reachable a moment later.
func waitForBucket(
	ctx context.Context,
	store storage.Store,
	ensure bool,
	log *slog.Logger,
) error {
	if !ensure {
		return nil
	}

	const (
		attempts = 10
		backoff  = 500 * time.Millisecond
	)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := store.EnsureBucket(ctx); err == nil {
			if attempt > 1 {
				log.Info("object store became available", "attempts", attempt)
			}
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return fmt.Errorf("object store unavailable after %d attempts: %w", attempts, lastErr)
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
// Extracted so the wiring is testable. A guard that exists only inline in the
// chain can be deleted without any test noticing.
func guardedHandler(mux http.Handler, tokens []string) http.Handler {
	return serviceauth.NewTokenSet(tokens).HTTP(mux, probePaths)
}

func buildHandler(
	svc handler.MediaService,
	store storage.Store,
	db *sql.DB,
	log *slog.Logger,
	inboundTokens []string,
) http.Handler {
	mux := http.NewServeMux()

	// Readiness covers both dependencies. Without the object store a client can
	// read metadata but can never upload, so the service is not ready.
	handler.NewHealthHandler(func(ctx context.Context) error {
		if err := database.Ping(ctx, db); err != nil {
			return err
		}
		return store.Ping(ctx)
	}, log).RegisterRoutes(mux)

	handler.NewMediaHandler(svc, log).RegisterRoutes(mux)

	// Order matters: recover first so it also catches panics raised by the
	// logging and header middleware.
	return wrapChain(mux, log, inboundTokens)
}

// wrapChain is the middleware chain, taking the mux rather than the full
// dependency graph.
//
// Extracted so the chain is reachable from a test. buildHandler needs a wired
// service, database and storage, so a test could not otherwise reach the chain —
// and deleting the guard from it left the suite green until this existed.
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
