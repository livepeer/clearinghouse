#!/bin/sh
set -eu

load_env() {
  if [ -f "$1" ]; then
    # .env files may contain empty assignments or comments; do not use set -u while sourcing.
    set +u
    set -a
    # shellcheck disable=SC1090
    . "$1"
    set +a
    set -u
  fi
}

# Collector config first; auth0-provisioner output second (bootstrap writes .env.livepeer).
load_env /service/.env
load_env "${ENV_LIVEPEER_FILE:-/service/.env.livepeer}"

# Map auth0-provisioner names → builder-api env (only when unset).
: "${AUTH0_SIGNER_M2M_CLIENT_ID:=${DEMO_APP_AUTH0_M2M_CLIENT_ID:-}}"
: "${AUTH0_SIGNER_M2M_CLIENT_SECRET:=${DEMO_APP_AUTH0_M2M_CLIENT_SECRET:-}}"
: "${AUTH0_AUDIENCE:=${DEMO_APP_AUTH0_AUDIENCE:-livepeer-clearinghouse}}"
export AUTH0_SIGNER_M2M_CLIENT_ID AUTH0_SIGNER_M2M_CLIENT_SECRET AUTH0_AUDIENCE

# Single-org stacks set OPENMETER_URL; multi-tenant stacks may use only
# KONNECT_CREDENTIALS_URL (collector looks up per-client Ingest SPATs).
if [ -z "${OPENMETER_URL:-}" ] && [ -z "${KONNECT_CREDENTIALS_URL:-}" ]; then
  echo "entrypoint: OPENMETER_URL or KONNECT_CREDENTIALS_URL is required" >&2
  exit 1
fi

openmeter_mgmt_url=""
if [ -n "${OPENMETER_URL:-}" ]; then
  base="${OPENMETER_URL%/}"

  # Management API base for builder-api (customers/subscriptions/entitlements) — never the
  # events ingestion path. Strip any events suffix so REST calls hit `<base>/customers`.
  openmeter_mgmt_url="$base"
  case "$base" in
    */api/v1/events) openmeter_mgmt_url="${base%/api/v1/events}" ;;
    */events) openmeter_mgmt_url="${base%/events}" ;;
  esac

  # Events ingestion URL for the benthos collector.
  case "$base" in
    */events)
      export OPENMETER_URL="$base"
      ;;
    *)
      if printf '%s' "$base" | grep -Eq '(^|\.)konghq\.com'; then
        export OPENMETER_URL="${base}/events"
      else
        export OPENMETER_URL="${base}/api/v1/events"
      fi
      ;;
  esac
fi

benthos_pid=""
builder_pid=""

cleanup() {
  [ -n "$builder_pid" ] && kill "$builder_pid" 2>/dev/null || true
  [ -n "$benthos_pid" ] && kill "$benthos_pid" 2>/dev/null || true
  wait 2>/dev/null || true
}

trap cleanup INT TERM

/usr/local/bin/benthos -c /config.yaml &
benthos_pid=$!

if [ -x /usr/local/bin/builder-api ]; then
  if [ -n "${AUTH0_MGMT_CLIENT_ID:-}" ] && [ -n "${AUTH0_MGMT_CLIENT_SECRET:-}" ]; then
    if [ -n "$openmeter_mgmt_url" ]; then
      OPENMETER_URL="$openmeter_mgmt_url" /usr/local/bin/builder-api &
      builder_pid=$!
    else
      echo "builder-api: skipped — OPENMETER_URL unset (management API base required)" >&2
    fi
  else
    echo "builder-api: skipped — AUTH0_MGMT_CLIENT_ID/SECRET not set (re-run auth0-provisioner/provision/bootstrap.sh)" >&2
  fi
fi

# Exit when either child exits; tear down the sibling.
while :; do
  if ! kill -0 "$benthos_pid" 2>/dev/null; then
    wait "$benthos_pid" 2>/dev/null || true
    cleanup
    exit 1
  fi
  if [ -n "$builder_pid" ] && ! kill -0 "$builder_pid" 2>/dev/null; then
    wait "$builder_pid" 2>/dev/null || true
    cleanup
    exit 1
  fi
  sleep 1
done
