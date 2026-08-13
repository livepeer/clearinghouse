# go-livepeer remote signer — extends livepeer/go-livepeer:v0.8.11 (CUDA libs required by binary).
# Do not copy livepeer into debian:slim; the binary links libnppig.so and other CUDA NPP libs.
#
# Optional Turnkey mode: when TURNKEY_ORG_ID + API keys are set, the entrypoint
# exports an ephemeral UTC keystore at boot (same pattern as pymthouse signer-dmz).
#
# go-livepeer runs directly in this container. Only the signing HTTP port is EXPOSE'd.
# The CLI/admin port (-cliAddr) is bound to loopback inside the container.
FROM golang:1.25.12-bookworm AS turnkey-bootstrap-build

WORKDIR /src
COPY remote-signer/turnkey-bootstrap/go.mod ./go.mod
COPY remote-signer/turnkey-bootstrap/go.sum ./go.sum
RUN go mod download
COPY remote-signer/turnkey-bootstrap/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/signer-turnkey-bootstrap .

FROM livepeer/go-livepeer:v0.8.11

RUN mkdir -p /data /run/signer-bootstrap && \
    if ! command -v curl >/dev/null 2>&1; then \
      apt-get update && \
      apt-get install -y --no-install-recommends curl ca-certificates && \
      rm -rf /var/lib/apt/lists/*; \
    fi

COPY --from=turnkey-bootstrap-build /out/signer-turnkey-bootstrap /usr/local/bin/signer-turnkey-bootstrap
COPY remote-signer/scripts/cleanup-ephemeral-keystore.sh /usr/local/bin/cleanup-ephemeral-keystore.sh
COPY remote-signer/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/signer-turnkey-bootstrap /usr/local/bin/cleanup-ephemeral-keystore.sh

EXPOSE 8081
VOLUME ["/data"]

ENTRYPOINT ["/entrypoint.sh"]
