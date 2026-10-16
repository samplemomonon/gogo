package main

import (
	"github.com/ruangbercerita182-tech/product-listing-service/internal/metrics"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/service"
)

// metricsCache decorates a ProductCache with hit and miss counters.
//
// It exists because ProductCache is declared on the consumer side. That means the
// service can be given a counting cache without importing Prometheus, and the
// instrumentation can be added at the composition root without touching either
// the use case or the LRU. Wiring the counters here is the reason the port is
// worth having; hard-coding metrics into the service would have put a Prometheus
// dependency in the business logic to measure it.
type metricsCache struct {
	inner service.ProductCache
	stats *metrics.Metrics
}

// newMetricsCache returns cache wrapped so its hits and misses are counted.
func newMetricsCache(inner service.ProductCache, stats *metrics.Metrics) service.ProductCache {
	return &metricsCache{inner: inner, stats: stats}
}

func (c *metricsCache) Get(key string) (*model.Product, bool) {
	product, ok := c.inner.Get(key)
	if ok {
		c.stats.RecordCacheHit()
	} else {
		c.stats.RecordCacheMiss()
	}
	return product, ok
}

func (c *metricsCache) Set(key string, product *model.Product) {
	c.inner.Set(key, product)
}

func (c *metricsCache) Delete(key string) {
	c.inner.Delete(key)
}
