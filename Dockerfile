# syntax=docker/dockerfile:1.12
#
# Multi-stage, reproducible build:
#   * builder: static Go binary (CGO disabled, trimmed paths, stripped symbols),
#     cross-compiled for the target platform without emulation;
#   * runtime: distroless static image, non-root user (65532), no shell or
#     package manager, read-only friendly (state lives in /data).
# Base images are pinned by digest; update them with "make docker-pin".

ARG GO_IMAGE=golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src

# Dependencies first for better layer caching.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -buildid= \
        -X github.com/aeciopires/docker-hub-image-statistics/internal/version.Version=${VERSION} \
        -X github.com/aeciopires/docker-hub-image-statistics/internal/version.Commit=${COMMIT} \
        -X github.com/aeciopires/docker-hub-image-statistics/internal/version.Date=${BUILD_DATE}" \
      -o /out/docker-hub-image-statistics ./cmd/docker-hub-image-statistics

# Writable data directory owned by the non-root user (distroless has no shell to mkdir).
RUN mkdir -p /out/data && chown 65532:65532 /out/data && chmod 0750 /out/data

FROM ${RUNTIME_IMAGE}

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="docker-hub-image-statistics" \
      org.opencontainers.image.description="Collects and displays pull statistics of public Docker Hub images" \
      org.opencontainers.image.source="https://github.com/aeciopires/docker-hub-image-statistics" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=builder --chown=65532:65532 /out/data /data
COPY --from=builder --chmod=0555 /out/docker-hub-image-statistics /usr/local/bin/docker-hub-image-statistics

ENV DHIS_CACHE_SQLITE_PATH=/data/statistics.db \
    DHIS_CACHE_DISK_PATH=/data/statistics.json

USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/docker-hub-image-statistics", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/docker-hub-image-statistics"]
CMD ["serve"]
