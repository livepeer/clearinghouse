# OpenMeter Benthos collector: Kafka create_signed_ticket events -> Konnect/OpenMeter ingest.
# Includes a local Go provision sidecar for idempotent customer upsert (Kong sdk-konnect-go).
FROM golang:1.25.11-alpine AS provision-build

WORKDIR /src
COPY openmeter-collector/provision/go.mod openmeter-collector/provision/go.sum ./
RUN go mod download
COPY openmeter-collector/provision/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /provision-server .

FROM ghcr.io/openmeterio/benthos-collector:main-6b60ab6-1782310960 AS benthos

FROM alpine:3.21

RUN apk add --no-cache wget ca-certificates

WORKDIR /app

COPY --from=provision-build /provision-server /app/provision-server
COPY openmeter-collector/entrypoint.sh /entrypoint.sh
COPY openmeter-collector/collector.yaml /config.yaml
COPY --from=benthos /usr/local/bin/benthos /usr/local/bin/benthos

RUN chmod +x /entrypoint.sh /app/provision-server

ENV PROVISION_PORT=8091
ENV OPENMETER_DEFAULT_PLAN_KEY=clearinghouse_default_ppu

HEALTHCHECK --interval=10s --timeout=3s --retries=6 --start-period=10s \
  CMD wget -q -O- http://127.0.0.1:8091/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
