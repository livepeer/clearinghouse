#!/bin/sh
set -eu

if [ -f /service/.env ]; then
  set -a
  # shellcheck disable=SC1091
  . /service/.env
  set +a
fi

SIGNER_NETWORK="${SIGNER_NETWORK:-arbitrum-one-mainnet}"
SIGNER_PORT="${SIGNER_PORT:-8081}"
ETH_RPC_URL="${ETH_RPC_URL:-https://arb1.arbitrum.io/rpc}"
KAFKA_BROKERS="${KAFKA_BROKERS:-kafka:9092}"
KAFKA_GATEWAY_TOPIC="${KAFKA_GATEWAY_TOPIC:-livepeer-gateway-events}"

# Railway injects PORT. Local compose leaves it unset and uses SIGNER_PORT (8081).
if [ -n "${PORT:-}" ]; then
  HTTP_PORT="$PORT"
else
  HTTP_PORT="$SIGNER_PORT"
fi

TURNKEY_MODE=0
if [ -n "${TURNKEY_ORG_ID:-}" ] && [ -n "${TURNKEY_API_PUBLIC_KEY:-}" ] && [ -n "${TURNKEY_API_PRIVATE_KEY:-}" ]; then
  TURNKEY_MODE=1
fi

if [ "$TURNKEY_MODE" = "1" ]; then
  /usr/local/bin/signer-turnkey-bootstrap || {
    echo "entrypoint: turnkey keystore bootstrap failed" >&2
    exit 1
  }
  _resolved_addr_file="${SIGNER_ADDRESS_OUT:-/run/signer-bootstrap/signer-eth-addr}"
  if [ ! -s "$_resolved_addr_file" ]; then
    echo "entrypoint: turnkey bootstrap did not write signer address to ${_resolved_addr_file}" >&2
    exit 1
  fi
  SIGNER_ETH_ADDR="$(tr -d '[:space:]' <"$_resolved_addr_file")"
  export SIGNER_ETH_ADDR
  SIGNER_ETH_KEYSTORE_PATH=/data/keystore
elif [ ! -f /data/.eth-password ]; then
  echo "" >/data/.eth-password
fi

if [ -z "${SIGNER_ETH_KEYSTORE_PATH:-}" ] && [ -d /data/keystore ]; then
  SIGNER_ETH_KEYSTORE_PATH=/data/keystore
fi

set -- \
  -remoteSigner \
  "-network=${SIGNER_NETWORK}" \
  "-httpAddr=0.0.0.0:${HTTP_PORT}" \
  "-cliAddr=127.0.0.1:4935" \
  "-ethUrl=${ETH_RPC_URL}" \
  "-ethPassword=/data/.eth-password" \
  "-datadir=/data" \
  -v=99 \
  -monitor \
  "-kafkaBootstrapServers=${KAFKA_BROKERS}" \
  "-kafkaGatewayTopic=${KAFKA_GATEWAY_TOPIC}"

if [ -n "${REMOTE_SIGNER_WEBHOOK_URL:-}" ]; then
  if [ -z "${WEBHOOK_SECRET:-}" ]; then
    echo "entrypoint: WEBHOOK_SECRET is required when REMOTE_SIGNER_WEBHOOK_URL is set" >&2
    exit 1
  fi
  set -- "$@" \
    "-remoteSignerWebhookUrl=${REMOTE_SIGNER_WEBHOOK_URL}" \
    "-remoteSignerWebhookHeaders=Authorization:Bearer ${WEBHOOK_SECRET}"
else
  echo "entrypoint: WARNING: starting remote signer without identity webhook authorization" >&2
fi

if [ -n "${SIGNER_ETH_ADDR:-}" ]; then
  set -- "$@" "-ethAcctAddr=${SIGNER_ETH_ADDR}"
fi

if [ -n "${SIGNER_ETH_KEYSTORE_PATH:-}" ]; then
  set -- "$@" "-ethKeystorePath=${SIGNER_ETH_KEYSTORE_PATH}"
fi

if [ "${SIGNER_REMOTE_DISCOVERY:-0}" = "1" ] || [ "${SIGNER_REMOTE_DISCOVERY:-0}" = "true" ]; then
  set -- "$@" -remoteDiscovery=true
  if [ -n "${ORCH_WEBHOOK_URL:-}" ]; then
    set -- "$@" "-orchWebhookUrl=${ORCH_WEBHOOK_URL}"
  fi
  if [ -n "${LIVE_AI_CAP_REPORT_INTERVAL:-}" ]; then
    set -- "$@" "-liveAICapReportInterval=${LIVE_AI_CAP_REPORT_INTERVAL}"
  fi
fi

echo "entrypoint: starting livepeer remote-signer on 0.0.0.0:${HTTP_PORT}" >&2

if [ "$TURNKEY_MODE" != "1" ]; then
  exec /usr/local/bin/livepeer "$@"
fi

/usr/local/bin/livepeer "$@" &
LIVEPEER_PID=$!

i=0
ready_timeout="${SIGNER_READY_TIMEOUT_SECONDS:-300}"
ready=0
while [ "$i" -lt "$ready_timeout" ]; do
  if ! kill -0 "$LIVEPEER_PID" 2>/dev/null; then
    echo "entrypoint: livepeer (pid $LIVEPEER_PID) exited before becoming ready" >&2
    wait "$LIVEPEER_PID" 2>/dev/null || true
    exit 1
  fi
  if curl -sf -X POST "http://127.0.0.1:${HTTP_PORT}/sign-orchestrator-info" \
    -H "Content-Type: application/json" \
    -d "{}" >/dev/null 2>&1; then
    ready=1
    break
  fi
  i=$((i + 1))
  sleep 1
done

if [ "$ready" -ne 1 ]; then
  echo "entrypoint: livepeer did not become ready on 127.0.0.1:${HTTP_PORT} within ${ready_timeout}s" >&2
  if kill -0 "$LIVEPEER_PID" 2>/dev/null; then
    kill "$LIVEPEER_PID" 2>/dev/null || true
    wait "$LIVEPEER_PID" 2>/dev/null || true
  fi
  exit 1
fi

if ! /usr/local/bin/cleanup-ephemeral-keystore.sh; then
  echo "entrypoint: failed to cleanup ephemeral keystore artifacts" >&2
  if kill -0 "$LIVEPEER_PID" 2>/dev/null; then
    kill "$LIVEPEER_PID" 2>/dev/null || true
    wait "$LIVEPEER_PID" 2>/dev/null || true
  fi
  exit 1
fi

wait "$LIVEPEER_PID"
