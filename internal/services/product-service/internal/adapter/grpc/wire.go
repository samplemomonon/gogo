//go:build wireinject

package grpc

import (
	"github.com/google/wire"
	"product-service/internal/ports"
)

// CategoryClientSet provides the category gRPC client.
var CategoryClientSet = wire.NewSet(
	NewCategoryClient,
	wire.Bind(new(ports.CategoryLookup), new(*CategoryClient)),
)

// MediaClientSet provides the media gRPC client.
var MediaClientSet = wire.NewSet(
	NewMediaClient,
	wire.Bind(new(ports.MediaLookup), new(*MediaClient)),
)