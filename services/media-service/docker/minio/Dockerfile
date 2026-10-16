# MinIO is built from source rather than pulled from Docker Hub.
#
# MinIO Community Edition stopped publishing prebuilt binaries on 2025-10-23,
# and the minio/minio and minio/mc repositories were removed from Docker Hub on
# 2026-09-11, so `docker pull minio/minio` fails with "repository does not
# exist". Upstream's own README now says to install from source. The tag below
# is the last release MinIO published (2025-10-15), which carries the fix for
# CVE-2025-62506.
#
# The result is an ordinary local image, so the compose stack is unchanged.
FROM golang:1.27-alpine AS builder

ARG MINIO_VERSION=RELEASE.2025-10-15T17-29-55Z

# go install resolves the non-semver release tag and produces a static binary,
# so no C toolchain is needed.
RUN CGO_ENABLED=0 go install github.com/minio/minio@${MINIO_VERSION}

# --- runtime stage ---
FROM alpine:3.20

# ca-certificates for outbound TLS; curl for the compose healthcheck; tzdata so
# log timestamps render in local time.
RUN apk add --no-cache ca-certificates curl tzdata \
    && adduser -D -u 1000 -H minio \
    && mkdir -p /data \
    && chown minio:minio /data

COPY --from=builder /go/bin/minio /usr/local/bin/minio

USER 1000:1000
WORKDIR /data

EXPOSE 9000 9001

HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -fsS http://localhost:9000/minio/health/live || exit 1

# Overridden by compose; these are the defaults MinIO expects.
ENV MINIO_ROOT_USER=minioadmin \
    MINIO_ROOT_PASSWORD=minioadmin

ENTRYPOINT ["/usr/local/bin/minio"]
CMD ["server", "/data", "--console-address", ":9001"]
