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

if [ ! -f /data/.eth-password ]; then
  echo "" >/data/.eth-password
fi

if [ -z "${SIGNER_ETH_KEYSTORE_PATH:-}" ] && [ -d /data/keystore ]; then
  SIGNER_ETH_KEYSTORE_PATH=/data/keystore
fi

set -- \
  -remoteSigner \
  "-network=${SIGNER_NETWORK}" \
  "-httpAddr=0.0.0.0:${SIGNER_PORT}" \
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
  # Allow running the signer without a webhook (local alive checks only, not production).
  set -- "$@" -remoteSignerAllowNoAuth
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

echo "entrypoint: starting livepeer remote-signer on 0.0.0.0:${SIGNER_PORT}" >&2
exec /usr/local/bin/livepeer "$@"
