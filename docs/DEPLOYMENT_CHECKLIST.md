# Circuit Breaker Deployment Checklist

## Pre-Deployment

- [ ] Review code changes in `internal/bootstrap/bootstrap.go`
- [ ] Verify all tests pass: `go test -race ./internal/shared/breaker/... ./internal/bootstrap/...`
- [ ] Build binary: `go build -o bin/gateway cmd/server/main.go`
- [ ] Review alert thresholds in `deploy/prometheus/circuit_breaker_alerts.yml`

## Deployment Steps

### 1. Deploy Gateway Updates
```bash
# Build and push image
docker build -t your-registry/gateway:v1.2.0 .
docker push your-registry/gateway:v1.2.0

# Update deployment
kubectl set image deployment/gateway gateway=your-registry/gateway:v1.2.0
kubectl rollout status deployment/gateway
```

### 2. Configure Prometheus Alerts
```bash
# Apply alert rules
kubectl apply -f deploy/prometheus/circuit_breaker_alerts.yml

# Verify rules loaded
kubectl exec -it prometheus-0 -- promtool check rules /etc/prometheus/rules/circuit_breaker_alerts.yml
```

### 3. Import Grafana Dashboard
```bash
# Via UI: Import deploy/grafana/circuit_breaker_dashboard.json
# Or via API:
curl -X POST http://grafana/api/dashboards/db \
  -H "Content-Type: application/json" \
  -d @deploy/grafana/circuit_breaker_dashboard.json
```

### 4. Configure Alerting
```bash
# Set up PagerDuty/Slack integration in Alertmanager
kubectl edit configmap alertmanager-config
```

## Post-Deployment Verification

### Metrics Exposed
```bash
# Check metrics endpoint
curl http://gateway:8080/metrics | grep catalog_breaker

# Expected output:
# gogo_catalog_breaker_state 0
# gogo_catalog_breaker_successes_total 123
# gogo_catalog_breaker_failures_total 0
# gogo_catalog_breaker_rejects_total 0
# gogo_catalog_breaker_trips_total 0
```

### Health Check Integration
```bash
# Verify readiness includes breaker state
curl http://gateway:8080/readiness
# Should return 200 OK when breaker is closed

# Test with catalog down
kubectl scale deployment catalog --replicas=0
# Wait for breaker to open, then check readiness
curl http://gateway:8080/readiness
# Should return 503 when breaker is open
```

### Debug Endpoint
```bash
curl http://gateway:8080/debug/vars | jq '.catalog_breaker'
# Should show: {"state":"closed","successes":N,"failures":0,...}
```

### Alert Testing
```bash
# Trigger test alert
kubectl scale deployment catalog --replicas=0
# Wait 1 minute
# Verify alert fires in Alertmanager/PagerDuty
```

## Rollback Plan

If issues occur:
```bash
# Rollback deployment
kubectl rollout undo deployment/gateway

# Remove alert rules (if needed)
kubectl delete -f deploy/prometheus/circuit_breaker_alerts.yml
```

## Training & Documentation

- [ ] Share runbook with ops team: `docs/runbooks/circuit-breaker-catalog.md`
- [ ] Share quick reference: `docs/CIRCUIT_BREAKER_QUICK_REF.md`
- [ ] Schedule training session for incident response
- [ ] Add alert runbook links to PagerDuty

## Monitoring Setup

- [ ] Verify Prometheus scraping metrics every 15s
- [ ] Confirm Grafana dashboard displays correctly
- [ ] Test alert notification delivery
- [ ] Set up on-call rotation for critical alerts

## Success Criteria

- [ ] All gateway pods report metrics
- [ ] Readiness probe reflects breaker state
- [ ] Alerts fire correctly during test scenario
- [ ] Dashboard shows real-time breaker state
- [ ] Ops team trained on runbook procedures

## Notes

Circuit breaker configuration:
- Max failures: 5 consecutive
- Cooldown: 5 seconds
- Max probes: 1

SLA targets:
- Availability: 99.95%
- Success rate: 99.5%
