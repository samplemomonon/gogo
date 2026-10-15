// Package messaging provides Kafka producer and outbox recorder adapters.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// ProducerConfig describes how to reach the broker.
type ProducerConfig struct {
	Brokers        []string
	ClientID       string
	PublishTimeout time.Duration
	Acks           kgo.Acks
}

// KafkaProducer implements ports.KafkaEventPublisher using franz-go.
type KafkaProducer struct {
	client  *kgo.Client
	timeout time.Duration
	log     *slog.Logger
}

// NewKafkaProducer creates a new Kafka producer.
func NewKafkaProducer(cfg ProducerConfig, log *slog.Logger) (*KafkaProducer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("at least one Kafka broker is required")
	}
	timeout := cfg.PublishTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	acks := cfg.Acks
	if acks == (kgo.Acks{}) {
		acks = kgo.AllISRAcks()
	}
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = "product-service"
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(acks),
		kgo.RecordRetries(5),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.ProducerOnDataLossDetected(func(topic string, partition int32) {
			log.Error("kafka reported data loss, producing has stopped",
				"topic", topic, "partition", partition)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}

	return &KafkaProducer{client: client, timeout: timeout, log: log}, nil
}

// Close flushes and closes the producer.
func (p *KafkaProducer) Close(ctx context.Context) error {
	flushErr := p.client.Flush(ctx)
	p.client.Close()
	if flushErr != nil {
		p.log.Error("flushing kafka producer failed", "error", flushErr)
		return fmt.Errorf("flush kafka producer: %w", flushErr)
	}
	return nil
}

// Append publishes an event to Kafka.
func (p *KafkaProducer) Append(ctx context.Context, event *product.DomainEvent) error {
	body, err := event.Marshal()
	if err != nil {
		return permanent(err)
	}

	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	results := p.client.ProduceSync(publishCtx, &kgo.Record{
		Topic:     event.Topic,
		Key:       []byte(event.PartitionKey),
		Value:     body,
		Timestamp: event.OccurredAt,
		Headers: []kgo.RecordHeader{
			{Key: "event_type", Value: []byte(event.EventType)},
			{Key: "event_id", Value: []byte(event.EventID)},
			{Key: "aggregate_type", Value: []byte(event.AggregateType)},
			{Key: "schema_version", Value: []byte(strconv.Itoa(event.SchemaVersion))},
			{Key: "occurred_at_unix_ms", Value: []byte(strconv.FormatInt(unixMillis(event.OccurredAt), 10))},
		},
	})

	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("publish %s to %s: %w", event.EventType, event.Topic, err)
	}
	return nil
}

// Ping checks broker connectivity.
func (p *KafkaProducer) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := p.client.Ping(pingCtx); err != nil {
		return fmt.Errorf("ping kafka: %w", err)
	}
	return nil
}

// Compile-time interface check
var _ ports.KafkaEventPublisher = (*KafkaProducer)(nil)

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }
func permanent(err error) error         { return &permanentError{err: err} }

func IsPermanent(err error) bool {
	var target *permanentError
	return errors.As(err, &target)
}
