package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer sends the dead-letter records the consumer cannot apply.
//
// It is a producer rather than a consumer-side feature because the dead-letter
// topic is a normal topic: anything that can read the product topic can read the
// rejected records too, and they can be replayed once the code that could not
// handle them is fixed.
type Producer struct {
	client  *kgo.Client
	timeout time.Duration
	log     *slog.Logger
}

// NewProducer returns a producer connected to the configured brokers.
func NewProducer(brokers []string, clientID string, timeout time.Duration, log *slog.Logger) (*Producer, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("messaging: at least one Kafka broker is required")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if clientID == "" {
		clientID = "product-listing-service"
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		// Acknowledging from all in-sync replicas: a dead-letter record that a
		// majority of replicas lost is exactly the record nobody will ever look at.
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(5*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("messaging: create kafka producer: %w", err)
	}
	return &Producer{client: client, timeout: timeout, log: log}, nil
}

// Publish sends one record and waits for the broker to acknowledge it.
func (p *Producer) Publish(
	ctx context.Context,
	topic string,
	key, value []byte,
	headers map[string]string,
) error {
	recordHeaders := make([]kgo.RecordHeader, 0, len(headers))
	for name, headerValue := range headers {
		recordHeaders = append(recordHeaders, kgo.RecordHeader{Key: name, Value: []byte(headerValue)})
	}

	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	results := p.client.ProduceSync(publishCtx, &kgo.Record{
		Topic:   topic,
		Key:     key,
		Value:   value,
		Headers: recordHeaders,
	})
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("publish to %s: %w", topic, err)
	}
	return nil
}

// Close flushes anything buffered and releases the connections.
func (p *Producer) Close(ctx context.Context) error {
	if err := p.client.Flush(ctx); err != nil {
		p.log.Error("flushing dead-letter producer failed", "error", err)
	}
	p.client.Close()
	return nil
}

// Ping checks that the broker is reachable.
func (p *Producer) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	if err := p.client.Ping(pingCtx); err != nil {
		return fmt.Errorf("ping kafka: %w", err)
	}
	return nil
}
