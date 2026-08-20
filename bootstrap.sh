#!/usr/bin/env bash
#
# One-command clearinghouse bootstrap.
#
# Runs the two provisioners this repo already maintains and then emits the
# artifacts the SDK and platform consume:
#
#   1. auth0-provisioner/provision/bootstrap.sh   -> .env.livepeer (Auth0)
#   2. openmeter-collector/provision/bootstrap.sh -> OpenMeter/Konnect catalog
#   3. this script                                -> sdk-config.json
#
# Both provisioners are idempotent, so re-running is safe and only fills in
# what is missing. This script deliberately does not reimplement either of
# them: the catalog definition lives in openmeter-collector/provision, the
# Auth0 definition lives in auth0-provisioner/provision/apps.json, and each
# stays the single source of truth for its half.
#
# Requires: jq. Auth0 provisioning also needs the auth0 CLI and an active
#           `auth0 login` session; catalog provisioning needs kongctl.
#
# Usage:
#   ./bootstrap.sh [--app NAME] [--skip-auth0] [--skip-catalog] [--out DIR]
#
# Env:
#   OPENMETER_URL                 default https://us.api.konghq.com/v3/openmeter
#   OPENMETER_API_KEY             Konnect PAT (kpat_…) for catalog provisioning
#   OPENMETER_TRIAL_FEATURE_KEY   default network_spend
#   OPENMETER_USAGE_METER_KEY     optional; default derived from trial feature
#   SIGNER_PROXY_URL / SIGNER_PUBLIC_URL / REMOTE_SIGNER_WEBHOOK_URL
#                                 platform URLs baked into sdk-config.json
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTH0_PROVISIONER="$REPO_ROOT/auth0-provisioner/provision/bootstrap.sh"
CATALOG_PROVISIONER="$REPO_ROOT/openmeter-collector/provision/bootstrap.sh"

APP_NAME=""
SKIP_AUTH0=0
SKIP_CATALOG=0
OUT_DIR="$REPO_ROOT/auth0-provisioner/provision"

OPENMETER_URL="${OPENMETER_URL:-https://us.api.konghq.com/v3/openmeter}"
OPENMETER_TRIAL_FEATURE_KEY="${OPENMETER_TRIAL_FEATURE_KEY:-network_spend}"

# Placeholder platform URLs — these are deploy-time values the bootstrap
# cannot discover. Override via env once the platform is deployed.
SIGNER_PROXY_URL="${SIGNER_PROXY_URL:-https://your-platform.vercel.app/api/signer}"
SIGNER_PUBLIC_URL="${SIGNER_PUBLIC_URL:-https://signer.your-domain.com}"
REMOTE_SIGNER_WEBHOOK_URL="${REMOTE_SIGNER_WEBHOOK_URL:-https://your-platform.vercel.app/webhooks/remote-signer}"

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --app)          APP_NAME="${2:-}"; shift 2 ;;
    --skip-auth0)   SKIP_AUTH0=1; shift ;;
    --skip-catalog) SKIP_CATALOG=1; shift ;;
    --out)          OUT_DIR="${2:-}"; shift 2 ;;
    -h|--help)      sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)              die "unknown argument: $1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required"
mkdir -p "$OUT_DIR"
ENV_FILE="$OUT_DIR/.env.livepeer"
SDK_CONFIG="$OUT_DIR/sdk-config.json"

# --- 1. Auth0 --------------------------------------------------------------
if [ "$SKIP_AUTH0" = 1 ]; then
  info "== Auth0: skipped =="
else
  [ -x "$AUTH0_PROVISIONER" ] || [ -f "$AUTH0_PROVISIONER" ] \
    || die "missing $AUTH0_PROVISIONER"
  info "== Auth0 =="
  OUTPUT="$ENV_FILE" bash "$AUTH0_PROVISIONER"
fi

[ -f "$ENV_FILE" ] || die "no $ENV_FILE — run without --skip-auth0, or create it first"

# --- 2. OpenMeter / Konnect catalog ----------------------------------------
if [ "$SKIP_CATALOG" = 1 ]; then
  info "== Catalog: skipped =="
else
  [ -f "$CATALOG_PROVISIONER" ] || die "missing $CATALOG_PROVISIONER"
  if [ -z "${OPENMETER_API_KEY:-}" ] && [ -z "${KONGCTL_DEFAULT_KONNECT_PAT:-}" ]; then
    die "set OPENMETER_API_KEY or KONGCTL_DEFAULT_KONNECT_PAT for catalog provisioning (or pass --skip-catalog)"
  fi
  info "== OpenMeter catalog =="
  OPENMETER_URL="$OPENMETER_URL" bash "$CATALOG_PROVISIONER" catalog
fi

# --- 3. sdk-config.json ----------------------------------------------------
# Read a KEY=VALUE out of .env.livepeer without sourcing it: the file holds
# secrets and arbitrary values, and sourcing would execute them.
env_get() {
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n1
}

# App-scoped keys are prefixed with the upper-snake app name, matching
# env_prefix() in the Auth0 provisioner.
app_prefix() {
  printf '%s' "$1" | tr '[:lower:]' '[:upper:]' | sed -E 's/[^A-Z0-9]+/_/g; s/^_+|_+$//g'
}

if [ -z "$APP_NAME" ]; then
  APPS_JSON="$REPO_ROOT/auth0-provisioner/provision/apps.json"
  [ -f "$APPS_JSON" ] || die "missing $APPS_JSON; pass --app NAME"
  APP_NAME="$(jq -r '.apps[0].name // empty' "$APPS_JSON")"
  [ -n "$APP_NAME" ] || die "no apps defined in $APPS_JSON; pass --app NAME"
fi
PREFIX="$(app_prefix "$APP_NAME")"

AUTH0_DOMAIN="$(env_get AUTH0_DOMAIN)"
AUTH0_ISSUER="$(env_get AUTH0_ISSUER)"
AUTH0_JWKS_URL="$(env_get AUTH0_JWKS_URL)"
AUDIENCE="$(env_get "${PREFIX}_AUTH0_AUDIENCE")"
PUBLIC_CLIENT_ID="$(env_get "${PREFIX}_AUTH0_PUBLIC_CLIENT_ID")"

[ -n "$AUDIENCE" ] \
  || die "no ${PREFIX}_AUTH0_AUDIENCE in $ENV_FILE — is \"$APP_NAME\" the right app name?"
[ -n "$PUBLIC_CLIENT_ID" ] \
  || die "no ${PREFIX}_AUTH0_PUBLIC_CLIENT_ID in $ENV_FILE"

# The tenant-level keys are written only when the provisioner could detect the
# active Auth0 tenant. Emitting sdk-config.json with blanks here produces a file
# that looks valid and fails at runtime, so stop instead.
for required in AUTH0_DOMAIN AUTH0_ISSUER AUTH0_JWKS_URL; do
  eval "value=\${$required}"
  [ -n "$value" ] || die "no $required in $ENV_FILE — re-run Auth0 provisioning (\`auth0 tenants use <tenant>\` first) or fix the file before generating sdk-config.json"
done

jq -n \
  --arg domain "$AUTH0_DOMAIN" \
  --arg issuer "$AUTH0_ISSUER" \
  --arg jwksUrl "$AUTH0_JWKS_URL" \
  --arg clientId "$PUBLIC_CLIENT_ID" \
  --arg audience "$AUDIENCE" \
  --arg proxyUrl "$SIGNER_PROXY_URL" \
  --arg publicUrl "$SIGNER_PUBLIC_URL" \
  --arg webhookUrl "$REMOTE_SIGNER_WEBHOOK_URL" \
  --arg omUrl "${OPENMETER_URL%/}" \
  --arg trialFeatureKey "$OPENMETER_TRIAL_FEATURE_KEY" \
  '{
     auth0:        { domain: $domain, issuer: $issuer, jwksUrl: $jwksUrl,
                     clientId: $clientId, audience: $audience },
     signer:       { proxyUrl: $proxyUrl, publicUrl: $publicUrl, audience: $audience },
     remoteSigner: { webhookUrl: $webhookUrl },
     openmeter:    { url: $omUrl, trialFeatureKey: $trialFeatureKey }
   }' > "$SDK_CONFIG"

info "wrote $ENV_FILE"
info "wrote $SDK_CONFIG"
info ""
info "next: set OPENMETER_URL + OPENMETER_API_KEY and configure"
info "      REMOTE_SIGNER_WEBHOOK_URL + WEBHOOK_SECRET for usage JWT verification"
