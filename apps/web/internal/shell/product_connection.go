// Product platform connections and change feed for the gateway.
package shell

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"

	"gogo/internal/shared/config"
	"gogo/internal/shared/events"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/productevents"
)

// productPlatform is the gateway's set of connections to the services that own
// product data. They are four separate deployments with their own listeners, so each is
// dialed and closed independently — and each carries its own breaker, because the
// storefront must keep serving browse while search is down.
type productPlatform struct {
	write    *grpc.ClientConn
	browse   *grpc.ClientConn
	search   *grpc.ClientConn
	category *grpc.ClientConn

	conns []*grpc.ClientConn
}

// connectProductPlatform dials every service of the product platform.
//
// All four addresses are required: there is no in-process fallback any more, because the
// gateway no longer owns any part of the product domain (see ARCHITECTURE.md D-018).
// Failing at startup is the honest outcome — a gateway that booted without its product
// read model would serve an empty storefront that looks like a real answer.
//
// Connections are established in order and unwound on partial failure, so a failure on
// the third dial does not leak the first two.
func connectProductPlatform(cfg *config.AppConfig, logger *slog.Logger) (*productPlatform, error) {
	targets := []struct {
		name    productconsumer.Upstream
		address string
	}{
		{productconsumer.UpstreamWrite, cfg.ProductAddress},
		{productconsumer.UpstreamBrowse, cfg.ProductListingAddress},
		{productconsumer.UpstreamSearch, cfg.ProductSearchAddress},
		{productconsumer.UpstreamCategory, cfg.CategoryAddress},
	}

	var opened []*grpc.ClientConn
	for _, target := range targets {
		if target.address == "" {
			closeConnections(opened)
			return nil, fmt.Errorf("gateway: %s service address is not configured", target.name)
		}
		conn, err := grpcx.Dial(target.address, cfg.ProductPlatformServiceToken)
		if err != nil {
			closeConnections(opened)
			return nil, fmt.Errorf("gateway: dial %s service %q: %w", target.name, target.address, err)
		}
		opened = append(opened, conn)
		logger.Info("gateway connected to product platform service",
			"service", string(target.name), "address", target.address)
	}

	return &productPlatform{
		write:    opened[0],
		browse:   opened[1],
		search:   opened[2],
		category: opened[3],
		conns:    opened,
	}, nil
}

// Connections projects the platform into the shape the shared consumer kit takes.
func (p *productPlatform) Connections() productconsumer.Connections {
	return productconsumer.Connections{
		Write:    p.write,
		Browse:   p.browse,
		Search:   p.search,
		Category: p.category,
	}
}

// Close releases every connection the platform opened.
func (p *productPlatform) Close() { closeConnections(p.conns) }

// closeConnections closes a set of connections, ignoring one that is already closed. It
// serves the partial-failure path, where a caller has nothing better to do than release
// what it already acquired.
func closeConnections(conns []*grpc.ClientConn) {
	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// startProductChangeFeed subscribes the gateway to the platform's product change
// stream and republishes each record onto the shared in-process bus, so modules that
// react to "a product changed" need no knowledge of Kafka (see the productevents package
// for why the gateway consumes the stream at all).
//
// It returns a stop function. The feed is optional: with no Kafka brokers configured
// there is nothing to consume, and the gateway runs on cache TTLs alone rather than
// failing to boot — a development install should not require a broker to serve the storefront.
// Callers that need a guaranteed-fresh cache configure one.
func startProductChangeFeed(
	parent context.Context,
	cfg *config.AppConfig,
	bus events.Bus,
	logger *slog.Logger,
) (func(), error) {
	if len(cfg.KafkaBrokers) == 0 {
		logger.Warn("product change feed disabled: KAFKA_BROKERS is empty; island caches rely on their TTL")
		return func() {}, nil
	}

	publisher := productevents.NewPublisher(bus)
	consumer, err := productevents.NewConsumer(cfg.KafkaBrokers, productevents.ConsumerGroup, publisher.Publish, logger)
	if err != nil {
		return nil, fmt.Errorf("gateway: subscribe to %s: %w", productevents.Topic, err)
	}

	runCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := consumer.Run(runCtx); err != nil {
			logger.Error("product change feed stopped", "error", err)
		}
	}()
	logger.Info("product change feed subscribed", "topic", productevents.Topic, "group", productevents.ConsumerGroup)

	// Stop cancels the feed and waits for the poll loop to drain, so a shut-down
	// gateway does not leave a consumer group member behind holding partitions.
	return func() {
		cancel()
		<-done
	}, nil
}
