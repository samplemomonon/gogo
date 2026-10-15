//go:build wireinject

package messaging

import (
	"github.com/google/wire"
	"product-service/internal/ports"
)

// ProducerSet provides the Kafka producer.
var ProducerSet = wire.NewSet(
	NewKafkaProducer,
	wire.Bind(new(ports.KafkaEventPublisher), new(*KafkaProducer)),
)

// OutboxRecorderSet provides the outbox recorder for derived events.
var OutboxRecorderSet = wire.NewSet(
	NewOutboxRecorder,
	wire.Bind(new(ports.OutboxEventRecorder), new(*OutboxRecorder)),
)
