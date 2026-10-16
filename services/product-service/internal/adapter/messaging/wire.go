//go:build wireinject

package messaging

import (
	"github.com/google/wire"
	"product-service/internal/ports"
)

// OutboxRecorderSet provides the outbox recorder for derived events.
var OutboxRecorderSet = wire.NewSet(
	NewOutboxRecorder,
	wire.Bind(new(ports.OutboxEventRecorder), new(*OutboxRecorder)),
)
