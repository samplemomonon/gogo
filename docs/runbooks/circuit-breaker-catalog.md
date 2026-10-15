# Circuit Breaker Runbook - Catalog Service

## Overview
The gateway uses a circuit breaker to protect against cascading failures when the catalog service becomes unavailable. This runbook covers troubleshooting and recovery procedures.

## Architecture
- **Location**: Gateway (`cmd/server`) wraps all catalog gRPC calls
- **Thresholds**: 5 consecutive failures trip the breaker
- **Cooldown**: 5 seconds before attempting recovery
- **Probes**: 1 request allowed in half-open state

## Alert Response

### CRITICAL: CatalogCircuitBreakerOpen
**Impact**: No catalog requests processed. Product listings, search, and inventory unavailable.

**Immediate Actions**:
1. Check catalog service pods:
   ```bash
   kubectl get pods -l app=catalog -n production
   kubectl describe pod <failing-pod> -n production
   ```

2. Check catalog logs for errors:
   ```bash
   kubectl logs -l app=catalog -n production --tail=100 --timestamps
   ```

3. Verify database connectivity:
   ```bash
   kubectl exec -it <catalog-pod> -- pg_isready -h $DB_HOST
   ```

4. Check breaker state across cluster:
   ```bash
   curl -s http://gateway:8080/debug/vars | jq '.catalog_breaker'
   ```

**Recovery Options**:
- **Automatic**: Breaker attempts recovery after 5s cooldown
- **Manual scale**: `kubectl scale deployment catalog --replicas=5`
- **Database failover**: If DB is down, trigger failover procedure
- **Circuit reset**: Restart gateway pods (last resort)

### WARNING: CatalogCircuitBreakerHighFailureRate
**Impact**: Approaching threshold. May trip soon.

**Actions**:
1. Monitor catalog service health metrics
2. Check for upstream issues (database, network)
3. Review recent deployments
4. Consider preemptive scaling

### CRITICAL: CatalogCircuitBreakerOpenClusterWide
**Impact**: Full catalog outage affecting all gateway instances.

**Incident Response**:
1. Page catalog team on-call
2. Declare SEV-1 incident
3. Check catalog deployment status across all zones
4. Review database cluster health
5. Activate disaster recovery if needed

## Metrics Reference

| Metric | Description | Healthy Range |
|--------|-------------|---------------|
| `gogo_catalog_breaker_state` | 0=closed, 1=open, 2=half-open | 0 |
| `gogo_catalog_breaker_successes_total` | Successful catalog calls | Increasing |
| `gogo_catalog_breaker_failures_total` | Failed calls (transport errors) | < 1% |
| `gogo_catalog_breaker_rejects_total` | Calls rejected by open breaker | 0 |
| `gogo_catalog_breaker_trips_total` | Times breaker has opened | 0 |

## Health Check Behavior

The gateway readiness probe (`/readiness`) includes circuit breaker state:
- **Breaker CLOSED**: Gateway reports ready
- **Breaker OPEN**: Gateway reports NOT ready
- **Effect**: Load balancer stops routing traffic to instances with open breakers

This prevents user requests from hitting instances that cannot reach catalog.

## Common Scenarios

### Scenario 1: Database Connection Pool Exhausted
**Symptoms**: High failure rate, breaker opens
**Solution**: Scale catalog replicas or increase DB pool size

### Scenario 2: Network Partition
**Symptoms**: Breaker open on subset of gateway instances
**Solution**: Check network connectivity, may self-heal

### Scenario 3: Catalog Deployment Rollout
**Symptoms**: Transient failures during pod restarts
**Solution**: Normal. Breaker recovers automatically in 5-10s

### Scenario 4: Slow Queries
**Symptoms**: Timeouts leading to failures
**Solution**: Investigate slow queries, add indexes, optimize

## Debugging Commands

```bash
# Check breaker state on specific gateway instance
curl http://gateway-pod-ip:8080/debug/vars | jq '.catalog_breaker'

# View Prometheus metrics
curl http://gateway-pod-ip:8080/metrics | grep catalog_breaker

# Check recent failures in logs
kubectl logs -l app=gateway --tail=100 | grep "catalog.*error"

# Force breaker state inspection
kubectl exec -it gateway-pod -- /bin/sh
curl localhost:8080/debug/vars
```

## Escalation Path
1. On-call SRE (first responder)
2. Catalog team on-call
3. Infrastructure team (database issues)
4. Engineering manager (prolonged outages)

## Related Documentation
- [Catalog Service Architecture](../architecture/catalog.md)
- [Database Failover Procedures](../runbooks/database-failover.md)
- [Incident Response Playbook](../runbooks/incident-response.md)
