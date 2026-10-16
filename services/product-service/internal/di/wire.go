package di

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"product-service/internal/adapter/grpc"
	"product-service/internal/adapter/grpcserver"
	"product-service/internal/adapter/http"
	"product-service/internal/adapter/messaging"
	"product-service/internal/config"
	"product-service/internal/database"
	"product-service/internal/usecase/product"
)

// App holds all application components.
type App struct {
	Config          *config.Config
	DB              *sql.DB
	ProductHandler  *http.ProductHandler
	HealthHandler   *http.HealthHandler
	GRPCServer      *grpcserver.ProductServer
	CategoryClient  *grpc.CategoryClient
	MediaClient     *grpc.MediaClient
	OutboxRecorder  *messaging.OutboxRecorder
	ProductUseCases product.ProductUseCases
	Logger          *slog.Logger
}

// InitializeApp initializes the application with all dependencies.
func InitializeApp(cfg *config.Config, log *slog.Logger) (*App, func(), error) {
	app, cleanup, err := initializeApp(cfg, log)
	if err != nil {
		return nil, cleanup, err
	}
	// Run migrations after all dependencies are wired
	if err := runMigrations(context.Background(), app.DB, log); err != nil {
		return nil, cleanup, fmt.Errorf("run migrations: %w", err)
	}
	return app, cleanup, nil
}

// provideDBConfig extracts database config from config.Config.
func provideDBConfig(cfg *config.Config) string {
	return cfg.DSN()
}

// provideDBOptions extracts database pool options from config.Config.
func provideDBOptions(cfg *config.Config) database.Options {
	return database.Options{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnMaxLifetime,
	}
}

// provideCategoryClientConfig extracts category gRPC config from config.Config.
func provideCategoryClientConfig(cfg *config.Config) grpc.CategoryClientConfig {
	serviceToken := ""
	if len(cfg.InboundServiceTokens) > 0 {
		serviceToken = cfg.InboundServiceTokens[0]
	}
	return grpc.CategoryClientConfig{
		Target:       cfg.CategoryGRPCTarget,
		Timeout:      cfg.DependencyTimeout,
		ServiceToken: serviceToken,
	}
}

// provideMediaClientConfig extracts media gRPC config from config.Config.
func provideMediaClientConfig(cfg *config.Config) grpc.MediaClientConfig {
	return grpc.MediaClientConfig{
		Target:  cfg.MediaGRPCTarget,
		Timeout: cfg.DependencyTimeout,
	}
}

// provideContext provides a context for the application.
func provideContext() context.Context {
	return context.Background()
}

// provideHealthCheck provides the health check function.
func provideHealthCheck(db *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		return database.Ping(ctx, db)
	}
}

// runMigrations runs database migrations after connection is established.
func runMigrations(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	return database.Migrate(ctx, db, log)
}
