# Gogo product faker

Generate sample categories and products against the Gogo product platform. The
flow mirrors `cqrs-demo/product-faker`, adapted to Gogo's API and ownership:
categories are created through Category Service, then products through Product
Service. Product reads remain owned by Product Listing Service.

From the repository root, start the platform services and run the generator:

```sh
docker compose -f docker-compose.dev.yml up -d category-service product-service
bun run tools/product-faker/index.ts
```

The generator sends `Authorization: Bearer ...` to both services. Its default
token matches the local development stack. Set `PRODUCT_PLATFORM_SERVICE_TOKEN`
to override it when `INBOUND_SERVICE_TOKENS` is customized. Bun loads a root
`.env` file. The generator stops immediately if Category Service rejects the
credential.

Configuration:

| Variable | Default | Purpose |
| --- | --- | --- |
| `CATEGORY_BASE_URL` | `http://localhost:8086` | Category Service HTTP base URL |
| `PRODUCT_BASE_URL` | `http://localhost:8082` | Product Service HTTP base URL |
| `PRODUCT_PLATFORM_SERVICE_TOKEN` | `local-dev-service-token-not-a-secret` | Bearer credential accepted by Category and Product Service; override when the stack uses a custom token |
| `CATEGORY_COUNT` | `12` | Number of sample categories to create |
| `PRODUCT_COUNT` | `250` | Number of sample products to create |
| `CONCURRENCY` | `12` | Concurrent product requests |
| `DRY_RUN` | unset | `1` or `true` to skip API writes |

The generator retries transient server errors and rate limits, reports a small
number of individual failures, and exits nonzero when any item fails. Every run
creates new data; use a disposable development database for repeated runs.
