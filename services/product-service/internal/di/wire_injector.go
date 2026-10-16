//go:build wireinject

package di

import (
	"log/slog"

	"github.com/google/wire"

	"product-service/internal/adapter/grpc"
	"product-service/internal/adapter/grpcserver"
	"product-service/internal/adapter/http"
	"product-service/internal/adapter/messaging"
	"product-service/internal/adapter/repository"
	"product-service/internal/config"
	"product-service/internal/database"
	"product-service/internal/usecase/product"
)

func initializeApp(cfg *config.Config, log *slog.Logger) (*App, func(), error) {
	wire.Build(
		provideContext,
		provideDBConfig,
		provideDBOptions,
		database.Connect,
		repository.RepositorySet,
		provideCategoryClientConfig,
		provideMediaClientConfig,
		grpc.CategoryClientSet,
		grpc.MediaClientSet,
		messaging.OutboxRecorderSet,
		product.UseCaseSet,
		product.ProvideUseCaseDependencies,
		http.HandlerSet,
		provideHealthCheck,
		grpcserver.GRPCServerSet,
		wire.Struct(new(App), "*"),
	)
	return nil, nil, nil
}
