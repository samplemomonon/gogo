#!/bin/bash
# Circuit breaker testing against the product platform.
#
# The gateway holds one breaker per upstream (see internal/shared/productconsumer), so
# this script scales a platform deployment down and back up and watches the gateway's
# readiness flip. That is the behaviour worth proving end to end: a gateway that cannot
# reach its browse read model must leave the load balancer rather than serve an empty
# storefront that looks like a real answer.
#
# Usage:
#   scripts/test-circuit-breaker.sh
#   PRODUCT_DEPLOYMENT=product-listing scripts/test-circuit-breaker.sh
#
# It mutates a cluster (scaling a deployment to zero and back), so point it at a
# non-production one.
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
NAMESPACE="${NAMESPACE:-gogo}"
# The deployment to interrupt. Any of the four works; the browse read model is the one
# whose outage takes the storefront down, so it is the default.
PRODUCT_DEPLOYMENT="${PRODUCT_DEPLOYMENT:-product-listing}"
ORIGINAL_REPLICAS="${ORIGINAL_REPLICAS:-3}"

# readiness echoes the gateway's own verdict: ready means it can serve traffic.
readiness() {
  curl -s -o /dev/null -w "%{http_code}" "$GATEWAY_URL/readyz"
}

step() { echo; echo "=== $* ==="; }

scale() { kubectl -n "$NAMESPACE" scale deployment "$PRODUCT_DEPLOYMENT" --replicas="$1"; }

step "1. Initial state (breaker closed)"
echo "readiness: $(readiness) (200 = ready)"

step "2. Interrupting $PRODUCT_DEPLOYMENT"
scale 0
echo "   waiting for the breaker to trip (5 consecutive transport failures)..."
# One request per attempt; each failure counts toward the breaker, which trips at 5.
for _ in $(seq 1 6); do
  curl -s -o /dev/null "$GATEWAY_URL/" || true
  sleep 1
done

step "3. Gateway state with the dependency down"
STATUS="$(readiness)"
echo "readiness: $STATUS (503 = the gateway correctly left the load balancer)"
if [ "$STATUS" != "503" ]; then
  echo "WARNING: expected 503. A gateway that stays ready cannot serve the storefront."
fi

step "4. Restoring $PRODUCT_DEPLOYMENT"
scale "$ORIGINAL_REPLICAS"
kubectl -n "$NAMESPACE" wait --for=condition=ready "pod" -l "app=$PRODUCT_DEPLOYMENT" --timeout=120s
echo "   waiting for the breaker cooldown (5s) and a probe..."
sleep 8

step "5. Gateway state after recovery"
STATUS="$(readiness)"
echo "readiness: $STATUS (200 = back in rotation)"
if [ "$STATUS" != "200" ]; then
  echo "WARNING: the gateway did not recover; check the dependency's own health."
fi

echo
echo "=== Complete ==="
