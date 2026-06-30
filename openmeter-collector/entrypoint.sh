#!/bin/sh
set -eu

if [ -f /service/.env ]; then
  set -a
  # shellcheck disable=SC1091
  . /service/.env
  set +a
fi

if [ -z "${OPENMETER_URL:-}" ]; then
  echo "entrypoint: OPENMETER_URL is required" >&2
  exit 1
fi

base="${OPENMETER_URL%/}"
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

exec /usr/local/bin/benthos -c /config.yaml
