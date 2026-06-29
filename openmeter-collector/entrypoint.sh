#!/bin/sh
set -eu

if [ -f /service/.env ]; then
  set -a
  # shellcheck disable=SC1091
  . /service/.env
  set +a
fi

if [ -z "${OPENMETER_URL:-}" ] || [ -z "${OPENMETER_API_KEY:-}" ]; then
  echo "entrypoint: OPENMETER_URL and OPENMETER_API_KEY are required" >&2
  exit 1
fi

/app/provision-server &
PROVISION_PID=$!

cleanup() {
  if kill -0 "$PROVISION_PID" 2>/dev/null; then
    kill "$PROVISION_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# Wait for provision sidecar before Benthos starts posting events.
for _ in $(seq 1 30); do
  if wget -q -O- "http://127.0.0.1:${PROVISION_PORT:-8091}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

exec /usr/local/bin/benthos -c /config.yaml
