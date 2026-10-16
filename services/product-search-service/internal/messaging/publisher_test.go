// Package messaging_test pins the Kafka adapter's configuration contract and
// its failure behaviour against an unreachable broker. The tests never need a
// live Kafka: the constructor validates its inputs and the client fails fast
// against a closed port.
package messaging_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"product-search-service/internal/messaging"

	"io"
	"log/slog"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewProducerRequiresABroker pins the constructor's fail-fast rule: a
// producer with nowhere to send is a configuration error at boot, not a
// surprise at the first dead letter.
func TestNewProducerRequiresABroker(t *testing.T) {
	t.Parallel()

	_, err := messaging.NewProducer(nil, "client", time.Second, quietLogger())
	if err == nil {
		t.Fatal("NewProducer accepted an empty broker list")
	}
	if !strings.Contains(err.Error(), "broker") {
		t.Errorf("error %q should name the missing broker list", err)
	}
}

// TestProducerFailuresNameTheOperation pins the message contract against an
// unreachable broker: a publish failure names the topic, and a ping failure
// names Kafka, so a log line says what was attempted.
func TestProducerFailuresNameTheOperation(t *testing.T) {
	t.Parallel()

	// 127.0.0.1:1 is closed, and the short timeout bounds the attempt even if
	// something starts answering.
	producer, err := messaging.NewProducer([]string{"127.0.0.1:1"}, "test", 250*time.Millisecond, quietLogger())
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(func() { _ = producer.Close(context.Background()) })

	ctx := context.Background()

	if err := producer.Publish(ctx, "dead.letters", []byte("key"), []byte("value"), nil); err == nil {
		t.Error("Publish succeeded against an unreachable broker")
	} else if !strings.Contains(err.Error(), "publish to dead.letters") {
		t.Errorf("error %q should name the publish target", err)
	}

	if err := producer.Ping(ctx); err == nil {
		t.Error("Ping succeeded against an unreachable broker")
	} else if !strings.Contains(err.Error(), "ping kafka") {
		t.Errorf("error %q should name the ping step", err)
	}
}

// TestNewConsumerValidatesItsConfiguration pins the three required settings,
// so a misconfigured replica refuses to start rather than joining no group or
// consuming nothing.
func TestNewConsumerValidatesItsConfiguration(t *testing.T) {
	t.Parallel()

	base := messaging.ConsumerConfig{
		Brokers: []string{"127.0.0.1:1"},
		GroupID: "product-search",
		Topics:  []string{"product.events.v1"},
	}

	for _, tt := range []struct {
		name  string
		apply func(*messaging.ConsumerConfig)
		want  string
	}{
		{"no brokers", func(c *messaging.ConsumerConfig) { c.Brokers = nil }, "broker"},
		{"no group", func(c *messaging.ConsumerConfig) { c.GroupID = "" }, "group"},
		{"no topics", func(c *messaging.ConsumerConfig) { c.Topics = nil }, "topic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			tt.apply(&cfg)

			_, err := messaging.NewConsumer(cfg, nil, nil, quietLogger())
			if err == nil {
				t.Fatal("NewConsumer accepted an invalid configuration")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should name the missing %s", err, tt.want)
			}
		})
	}
}
