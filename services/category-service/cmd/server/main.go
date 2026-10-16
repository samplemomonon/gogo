// Command server runs the category REST and gRPC APIs.
package main

import (
	"category-service/internal/serviceauth"
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
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"category-service/gen/categorypb"
	"category-service/internal/config"
	"category-service/internal/database"
	"category-service/internal/grpcsrv"
	"category-service/internal/handler"
	"category-service/internal/repository"
	"category-service/internal/service"
	"category-service/internal/tenant"
	"category-service/internal/version"
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
	_, _ = fmt.Fprint(w, `Usage: category-service [flags]

Serves the category REST and gRPC APIs. REST remains the public API; gRPC
exposes the read side for internal callers. Configuration comes from the
environment; see the README for the full list of variables and their defaults.

Flags:
  -version    print the build version and exit
  -help       print this message and exit

Endpoints:
  GET    /health                              liveness probe
  GET    /readyz                              readiness probe (checks the database)
  GET    /version                             build information
  POST   /api/v1/categories                   create a category
  GET    /api/v1/categories                   list categories
  GET    /api/v1/categories/tree              list categories as a tree
  GET    /api/v1/categories/{id}              fetch a category
  PUT    /api/v1/categories/{id}              update a category
  DELETE /api/v1/categories/{id}              delete a category and its subtree
  GET    /api/v1/categories/{id}/children     list direct children
  GET    /api/v1/categories/{id}/ancestors    list ancestors, parent first
  GET    /api/v1/categories/{id}/impact       count what a delete would remove

gRPC (internal, port 9090 by default):
  categorypb.CategoryService   get, list, tree, batch existence check
  Writes are not exposed: only the owning service may mutate categories.
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

	svc := service.NewCategoryService(repository.NewCategoryRepository(db))

	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           buildHandler(svc, db, log, cfg.InboundServiceTokens),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	grpcServer := buildGRPCServer(svc, cfg.GRPCMaxMessageSize, cfg.InboundServiceTokens)

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
func buildGRPCServer(svc grpcsrv.CategoryService, maxMessageSize int, inboundTokens []string) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		// A panic in a gRPC handler exits the process: grpc-go runs each RPC on its
		// own goroutine and does not recover. Without these interceptors any
		// reachable handler bug is an outage rather than a failed request.
		grpc.ChainUnaryInterceptor(
			serviceauth.NewTokenSet(inboundTokens).Unary(),
			tenant.IncomingUnary(),
			grpcsrv.UnaryRecovery(slog.Default()),
		),
		grpc.StreamInterceptor(grpcsrv.StreamRecovery(slog.Default())),
	)

	categorypb.RegisterCategoryServiceServer(server, grpcsrv.NewServer(svc, slog.Default()))

	// A standard health service lets load balancers probe gRPC directly. It
	// reports SERVING as soon as the process is up: the database check lives in
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
// Extracted so the wiring is testable. A guard that exists only inline in the
// chain can be deleted without any test noticing.
func guardedHandler(mux http.Handler, tokens []string) http.Handler {
	return serviceauth.NewTokenSet(tokens).HTTP(mux, probePaths)
}

func buildHandler(svc handler.CategoryService, db *sql.DB, log *slog.Logger, inboundTokens []string) http.Handler {
	mux := http.NewServeMux()
	handler.NewHealthHandler(func(ctx context.Context) error {
		return database.Ping(ctx, db)
	}, log).RegisterRoutes(mux)
	handler.NewCategoryHandler(svc, log).RegisterRoutes(mux)

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
