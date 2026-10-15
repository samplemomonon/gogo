# Circuit Breaker Configuration Guide

## Current Configuration

The catalog circuit breaker is configured with the following parameters:

```go
// internal/shared/catalogconsumer/client.go
breaker.New(5, 5*time.Second, 1)
```

- **Max Failures**: 5 consecutive failures trip the breaker
- **Cooldown**: 5 seconds before attempting recovery
- **Max Probes**: 1 concurrent request in half-open state

## Tuning for E-commerce

### Recommended Adjustments by Operation Type

Different catalog operations have different latency requirements:

| Operation | Timeout | Max Failures | Cooldown | Rationale |
|-----------|---------|--------------|----------|-----------|
| Product Search | 500ms | 3 | 3s | User-facing, needs fast response |
| Product Detail | 1s | 5 | 5s | Current config (balanced) |
| Inventory Check | 2s | 5 | 10s | Can tolerate higher latency |
| Bulk Import | 30s | 10 | 30s | Background job, more resilient |

### Production-Tested Values

Based on industry standards for e-commerce:

**Aggressive (Low Latency):**
```go
breaker.New(3, 3*time.Second, 1)
```
- Trips faster (after 3 failures)
- Recovers faster (3s cooldown)
- Use for: Product search, homepage listings

**Balanced (Current):**
```go
breaker.New(5, 5*time.Second, 1)
```
- Good default for most operations
- Use for: Product details, category pages

**Tolerant (High Availability):**
```go
breaker.New(10, 10*time.Second, 2)
```
- More resilient to transient failures
- Allows 2 probes for faster recovery
- Use for: Admin operations, background jobs

## Timeout Configuration

### gRPC Client Timeout

Currently using default gRPC timeout. For ecommerce, recommended per-call timeouts:

```go
// Example implementation
ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
defer cancel()
product, err := client.Get(ctx, productID)
```

### Environment-Based Configuration

Consider making timeouts configurable:

```bash
# .env or config
CATALOG_TIMEOUT_MS=500
CATALOG_MAX_FAILURES=3
CATALOG_COOLDOWN_SEC=3
```

## SLA Targets

### E-commerce Industry Standards

| Metric | Target | Rationale |
|--------|--------|-----------|
| Availability | 99.95% | ~22 minutes downtime/month |
| Success Rate | 99.5% | Max 0.5% transport errors |
| p50 Latency | < 100ms | Fast user experience |
| p95 Latency | < 500ms | Acceptable for most users |
| p99 Latency | < 2s | Edge case tolerance |

### Circuit Breaker Impact on SLA

- **Open breaker** counts as downtime for affected instances
- **Fast failures** (rejects) prevent request queueing
- **Load balancer** removes unhealthy instances via readiness probe

## Monitoring Thresholds

### Alert Thresholds

```yaml
# Recommended Prometheus alert thresholds
- Success rate < 99.5% for 5 minutes → WARNING
- Success rate < 95% for 1 minute → CRITICAL
- Breaker open for 1 minute → CRITICAL
- Breaker trips > 5 in 5 minutes → WARNING
```

### Dashboard Panels

1. **Real-time state**: Current breaker state (closed/open/half-open)
2. **Success rate**: Rolling 5-minute window
3. **Request rate**: Successes, failures, rejects
4. **Trip frequency**: How often breaker opens
5. **Recovery time**: Time spent in open/half-open states

## Implementation Roadmap

### Phase 1: Current State ✅
- [x] Basic circuit breaker with metrics
- [x] Health check integration
- [x] Prometheus metrics
- [x] Alert rules
- [x] Dashboard
- [x] Runbook

### Phase 2: Recommended Improvements

#### A. Per-Operation Timeouts
Add context timeouts to catalog client methods:
```go
func (c *Client) GetWithTimeout(ctx context.Context, id string, timeout time.Duration) (*Product, error)
```

#### B. Latency Tracking
Add histogram metrics for p50, p95, p99:
```go
// Add to breaker package
histogram := prometheus.NewHistogramVec(
    prometheus.HistogramOpts{
        Name:    "gogo_catalog_request_duration_seconds",
        Buckets: prometheus.DefBuckets, // 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10
    },
    []string{"operation", "status"},
)
```

#### C. Adaptive Thresholds
Adjust breaker sensitivity based on time of day:
- Peak hours (9am-9pm): More aggressive (trip faster)
- Off-peak: More tolerant (allow more retries)

#### D. Fallback Responses
When breaker is open, return cached data instead of errors:
```go
if err == catalogconsumer.ErrCatalogUnavailable {
    // Return stale data from cache with warning
    return cache.GetStale(productID), nil
}
```

### Phase 3: Advanced Features

- **Rate limiting**: Prevent catalog overload
- **Bulkhead pattern**: Isolate failures by operation type
- **Retry with jitter**: Prevent thundering herd on recovery
- **Multi-region failover**: Route to backup region when primary fails

## Testing Recommendations

### Load Testing
```bash
# Simulate catalog outage
k6 run tests/load/catalog-outage.js

# Verify breaker opens at threshold
# Verify recovery works correctly
# Measure impact on user experience
```

### Chaos Engineering
```bash
# Kill catalog pods during peak traffic
kubectl delete pod -l app=catalog --force

# Verify:
# - Breaker trips correctly
# - Load balancer removes unhealthy gateways
# - System recovers when catalog returns
```

## References

- [Release It! - Circuit Breaker Pattern](https://pragprog.com/titles/mnee2/release-it-second-edition/)
- [Microsoft Azure - Circuit Breaker Pattern](https://learn.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker)
- [Netflix Hystrix - Configuration](https://github.com/Netflix/Hystrix/wiki/Configuration)
