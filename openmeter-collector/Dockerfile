# OpenMeter Benthos collector + Clearinghouse Builder API (Go).
FROM golang:1.25-bookworm AS builder-api-build
WORKDIR /src
COPY openmeter-collector/builder-api/ ./openmeter-collector/builder-api/
WORKDIR /src/openmeter-collector/builder-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /builder-api ./cmd/builder-api

FROM ghcr.io/openmeterio/benthos-collector:main-6b60ab6-1782310960

COPY --from=builder-api-build /builder-api /usr/local/bin/builder-api
COPY openmeter-collector/collector.yaml /config.yaml
COPY openmeter-collector/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
