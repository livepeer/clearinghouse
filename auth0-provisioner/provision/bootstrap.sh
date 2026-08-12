#!/usr/bin/env bash
#
# Scaffold the clearinghouse Auth0 client pair(s) using your `auth0 login` session.
#
# Idempotent: creates only what is missing — the resource server (API), the public
# (native, device-flow) + M2M (confidential) client pair, and their client grants.
# Matches existing objects by identifier (API) and name (clients), or by optional
# public.client_id / m2m.client_id in apps.json. Re-running reuses existing ids,
# updates grant scopes in place, and never duplicates. If provisioning an app
# fails mid-way, objects created during that run are rolled back (pre-existing
# clients/grants are left untouched).
#
# Requires: jq. The Auth0 CLI (https://github.com/auth0/auth0-cli) too — if it is
#           missing the script offers to install it locally (or set AUTH0_INSTALL=1
#           to auto-install, AUTH0_BIN_DIR to choose where).
# Auth:     your active `auth0 login` session. Run `auth0 login` first (and
#           `auth0 tenants use <tenant>` to pick the tenant). No Management API
#           client id/secret needed — every call rides the CLI session via `auth0 api`.
#
# Usage:
#   auth0 login                 # one-time, interactive
#   ./bootstrap.sh              # scaffold everything in apps.json; writes .env.livepeer
#
# Config: apps.json (override with APPS=/path). Output: .env.livepeer (override OUTPUT=).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APPS="${APPS:-$SCRIPT_DIR/apps.json}"
OUTPUT="${OUTPUT:-$SCRIPT_DIR/.env.livepeer}"

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }

command -v jq >/dev/null 2>&1 || die "jq not found"
[ -f "$APPS" ] || die "apps config not found: $APPS"

# --- resolve the Auth0 CLI (PATH → local copy → offer to install) -----------
# The official installer drops the binary into the chosen dir (it is NOT added to
# PATH), so we always invoke it by path via $AUTH0_BIN once resolved.
AUTH0_BIN_DIR="${AUTH0_BIN_DIR:-$SCRIPT_DIR}"
AUTH0_INSTALL_URL="https://raw.githubusercontent.com/auth0/auth0-cli/main/install.sh"

confirm() { # confirm <prompt> <default:y|n> — true on yes
  local prompt="$1" default="${2:-n}" reply=""
  [ -t 0 ] || { [ "$default" = "y" ]; return; }
  printf '%s ' "$prompt" >&2
  read -r reply
  reply="${reply:-$default}"
  case "$reply" in [yY] | [yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

resolve_auth0_cli() {
  if command -v auth0 >/dev/null 2>&1; then AUTH0_BIN="auth0"; return; fi
  if [ -x "$AUTH0_BIN_DIR/auth0" ]; then AUTH0_BIN="$AUTH0_BIN_DIR/auth0"; return; fi

  info "auth0 CLI not found (https://github.com/auth0/auth0-cli)."
  local install="${AUTH0_INSTALL:-}"
  if [ -z "$install" ]; then
    confirm "Install the Auth0 CLI into $AUTH0_BIN_DIR now? [y/N]" n && install=1 || install=0
  fi
  [ "$install" = "1" ] || die "auth0 CLI required. Install it with:
  curl -sSfL $AUTH0_INSTALL_URL | sh -s -- -b \"$AUTH0_BIN_DIR\"
(or see https://github.com/auth0/auth0-cli), then re-run. Set AUTH0_INSTALL=1 to auto-install."

  command -v curl >/dev/null 2>&1 || die "curl not found — needed to install the auth0 CLI"
  info "installing auth0 CLI into $AUTH0_BIN_DIR ..."
  curl -sSfL "$AUTH0_INSTALL_URL" | sh -s -- -b "$AUTH0_BIN_DIR" >&2 || die "auth0 CLI install failed"
  [ -x "$AUTH0_BIN_DIR/auth0" ] || die "auth0 CLI install did not produce $AUTH0_BIN_DIR/auth0"
  AUTH0_BIN="$AUTH0_BIN_DIR/auth0"
  info "installed $("$AUTH0_BIN" --version 2>/dev/null | head -n1)"
}

ensure_auth0_session() {
  "$AUTH0_BIN" api get "clients?per_page=1&include_fields=true&fields=client_id" >/dev/null 2>&1 && return
  info "no active Auth0 session for '$AUTH0_BIN'."
  if [ -t 0 ] && confirm "Run '$AUTH0_BIN login' now? [Y/n]" y; then
    "$AUTH0_BIN" login || die "auth0 login failed"
    "$AUTH0_BIN" api get "clients?per_page=1&include_fields=true&fields=client_id" >/dev/null 2>&1 \
      || die "still no active session after login — check 'auth0 tenants use <tenant>'"
    return
  fi
  die "run '$AUTH0_BIN login' (and '$AUTH0_BIN tenants use <tenant>'), then re-run"
}

resolve_auth0_cli
ensure_auth0_session

# --- auth0 api helpers (Management API v2 passthrough; JSON body on stdout) ---
aapi_get()    { "$AUTH0_BIN" api get    "$1"; }
aapi_post()   { printf '%s' "$2" | "$AUTH0_BIN" api post  "$1"; }
aapi_patch()  { printf '%s' "$2" | "$AUTH0_BIN" api patch "$1"; }
aapi_delete() { "$AUTH0_BIN" api delete "$1" --force >/dev/null 2>&1 || true; }

# --- resource server (API) -------------------------------------------------
ensure_resource_server() {
  local identifier="$1" name="$2" signing_alg="$3" scopes_json="$4" existing_id body
  existing_id="$(aapi_get "resource-servers?per_page=100" \
    | jq -r --arg id "$identifier" '.[] | select(.identifier == $id) | .id' | head -n1)"
  if [ -n "$existing_id" ]; then
    body="$(jq -nc --argjson s "$scopes_json" '{scopes: $s}')"
    aapi_patch "resource-servers/$existing_id" "$body" >/dev/null
    info "resource server $identifier: updated"
    return
  fi
  body="$(jq -nc --arg name "$name" --arg id "$identifier" --arg alg "$signing_alg" --argjson s "$scopes_json" \
    '{name: $name, identifier: $id, signing_alg: $alg, allow_offline_access: true,
      skip_consent_for_verifiable_first_party_clients: true, scopes: $s}')"
  aapi_post "resource-servers" "$body" >/dev/null
  info "resource server $identifier: created"
}

# --- tenant device flow (RFC 8628) — best-effort ---------------------------
ensure_device_flow() {
  local audience="$1" body
  body="$(jq -nc --arg aud "$audience" \
    '{default_audience: $aud, device_flow: {charset: "base20", mask: "****-****"}}')"
  if aapi_patch "tenants/settings" "$body" >/dev/null 2>&1; then
    info "tenant: device flow enabled (default_audience=$audience)"
  else
    warn "tenant: could not set device-flow settings (session may lack update:tenant_settings) — skipping"
  fi
}

# --- clients ---------------------------------------------------------------
client_id_by_name() {
  aapi_get "clients?per_page=100&include_fields=true&fields=client_id,name" \
    | jq -r --arg n "$1" '.[] | select(.name == $n) | .client_id' | head -n1
}

client_id_exists() {
  aapi_get "clients/$1?include_fields=true&fields=client_id" \
    | jq -e -r '.client_id // empty' >/dev/null 2>&1
}

# ensure_client <name> <app_type> <auth_method> <grant_types_json> [extra_json] [configured_id]
# Sets ENSURED_CLIENT_ID and ENSURED_CLIENT_CREATED (0=reused, 1=created). Returns 1 on failure.
ensure_client() {
  local name="$1" app_type="$2" auth_method="$3" grants="$4" extra="${5:-"{}"}" configured_id="${6:-}" cid body
  ENSURED_CLIENT_CREATED=0

  if [ -n "$configured_id" ]; then
    if client_id_exists "$configured_id"; then
      cid="$configured_id"
      info "client \"$name\": using configured id $cid"
      ENSURED_CLIENT_ID="$cid"
      return 0
    fi
    warn "client \"$name\": configured id $configured_id not found — falling back to name lookup"
  fi

  cid="$(client_id_by_name "$name")"
  if [ -n "$cid" ]; then
    info "client \"$name\": exists ($cid)"
    ENSURED_CLIENT_ID="$cid"
    return 0
  fi

  body="$(jq -nc --arg name "$name" --arg t "$app_type" --arg am "$auth_method" \
    --argjson g "$grants" --argjson x "$extra" \
    '{name: $name, app_type: $t, token_endpoint_auth_method: $am, oidc_conformant: true, grant_types: $g} + $x')" \
    || return 1
  cid="$(aapi_post "clients" "$body" | jq -r '.client_id // empty')"
  [ -n "$cid" ] || return 1
  info "client \"$name\": created ($cid)"
  ENSURED_CLIENT_ID="$cid"
  ENSURED_CLIENT_CREATED=1
  return 0
}

client_secret() {
  aapi_get "clients/$1?include_fields=true&fields=client_secret" | jq -r '.client_secret // empty'
}

# --- client grants ---------------------------------------------------------
# Sets ENSURED_GRANT_ID and ENSURED_GRANT_CREATED (0=reused, 1=created). Returns 1 on failure.
ensure_client_grant() {
  local client_id="$1" audience="$2" scopes_json="$3" gid body resp
  ENSURED_GRANT_CREATED=0
  gid="$(aapi_get "client-grants?client_id=${client_id}&audience=${audience}" | jq -r '.[0].id // empty')"
  if [ -n "$gid" ]; then
    body="$(jq -nc --argjson s "$scopes_json" '{scope: $s}')" || return 1
    aapi_patch "client-grants/$gid" "$body" >/dev/null || return 1
    info "grant ($client_id -> $audience): updated"
    ENSURED_GRANT_ID="$gid"
    return 0
  fi
  body="$(jq -nc --arg c "$client_id" --arg a "$audience" --argjson s "$scopes_json" \
    '{client_id: $c, audience: $a, scope: $s}')" || return 1
  resp="$(aapi_post "client-grants" "$body")" || return 1
  gid="$(jq -r '.id // empty' <<<"$resp")"
  [ -n "$gid" ] || return 1
  info "grant ($client_id -> $audience): created"
  ENSURED_GRANT_ID="$gid"
  ENSURED_GRANT_CREATED=1
  return 0
}

# provision_app <app_json> — idempotent per app; rolls back objects created this run on failure.
provision_app() {
  local app="$1" name audience pub_scopes pub_callbacks pub_initiate m2m_scopes pub_extra
  local pub_id="" m2m_id="" m2m_secret="" pub_grant_id="" m2m_grant_id=""
  local created_pub=0 created_m2m=0 created_pub_grant=0 created_m2m_grant=0
  local pub_configured_id="" m2m_configured_id=""

  rollback_app() {
    info "rolling back \"$name\" (objects created this run only)"
    [ "$created_m2m_grant" = 1 ] && [ -n "$m2m_grant_id" ] && aapi_delete "client-grants/$m2m_grant_id"
    [ "$created_pub_grant" = 1 ] && [ -n "$pub_grant_id" ] && aapi_delete "client-grants/$pub_grant_id"
    [ "$created_m2m" = 1 ] && [ -n "$m2m_id" ] && aapi_delete "clients/$m2m_id"
    [ "$created_pub" = 1 ] && [ -n "$pub_id" ] && aapi_delete "clients/$pub_id"
  }

  name="$(jq -r '.name' <<<"$app")"
  audience="$(jq -r '.audience // empty' <<<"$app")"
  [ -n "$audience" ] || audience="$RS_ID"
  pub_scopes="$(jq -c '.public.grant_scopes' <<<"$app")"
  pub_callbacks="$(jq -c '.public.callbacks // []' <<<"$app")"
  pub_initiate="$(jq -r '.public.initiate_login_uri // ""' <<<"$app")"
  m2m_scopes="$(jq -c '.m2m.grant_scopes' <<<"$app")"
  pub_configured_id="$(jq -r '.public.client_id // empty' <<<"$app")"
  m2m_configured_id="$(jq -r '.m2m.client_id // empty' <<<"$app")"

  info "=== $name ==="

  pub_extra="$(jq -nc --argjson cb "$pub_callbacks" --arg iu "$pub_initiate" \
    '{is_first_party: true, callbacks: $cb} + (if $iu == "" then {} else {initiate_login_uri: $iu} end)')" \
    || { rollback_app; die "failed to build public client config for \"$name\""; }

  ensure_client "$name Public" native none \
    '["urn:ietf:params:oauth:grant-type:device_code","refresh_token"]' "$pub_extra" "$pub_configured_id" \
    || { rollback_app; die "failed to ensure public client for \"$name\""; }
  pub_id="$ENSURED_CLIENT_ID"
  created_pub="$ENSURED_CLIENT_CREATED"

  ensure_client "$name M2M" non_interactive client_secret_post '["client_credentials"]' '{}' "$m2m_configured_id" \
    || { rollback_app; die "failed to ensure M2M client for \"$name\""; }
  m2m_id="$ENSURED_CLIENT_ID"
  created_m2m="$ENSURED_CLIENT_CREATED"

  m2m_secret="$(client_secret "$m2m_id")"
  [ -n "$m2m_secret" ] || { rollback_app; die "failed to read M2M secret for \"$name\""; }

  ensure_client_grant "$pub_id" "$audience" "$pub_scopes" \
    || { rollback_app; die "failed to ensure public client grant for \"$name\""; }
  pub_grant_id="$ENSURED_GRANT_ID"
  created_pub_grant="$ENSURED_GRANT_CREATED"

  ensure_client_grant "$m2m_id" "$audience" "$m2m_scopes" \
    || { rollback_app; die "failed to ensure M2M client grant for \"$name\""; }
  m2m_grant_id="$ENSURED_GRANT_ID"
  created_m2m_grant="$ENSURED_GRANT_CREATED"

  P="$(env_prefix "$name")"
  {
    printf '# %s\n' "$name"
    printf '%s_AUTH0_AUDIENCE=%s\n' "$P" "$audience"
    printf '%s_AUTH0_PUBLIC_CLIENT_ID=%s\n' "$P" "$pub_id"
    printf '%s_AUTH0_M2M_CLIENT_ID=%s\n' "$P" "$m2m_id"
    printf '%s_AUTH0_M2M_CLIENT_SECRET=%s\n\n' "$P" "$m2m_secret"
  } >> "$OUTPUT"
}

env_prefix() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]' | sed -E 's/[^A-Z0-9]+/_/g; s/^_+|_+$//g'; }

# ensure_management_client <tenant_domain> — M2M for Builder API user provisioning.
ensure_management_client() {
  local domain="$1" name scopes_json mgmt_id mgmt_secret audience created
  if [ "$(jq -r '.managementClient.enabled // true' "$APPS")" != "true" ]; then
    info "management client: disabled in apps.json — skipping"
    return 0
  fi
  name="$(jq -r '.managementClient.name // "Clearinghouse Builder Management"' "$APPS")"
  scopes_json="$(jq -c '.managementClient.grant_scopes // ["create:users","read:users","update:users"]' "$APPS")"
  audience="https://${domain}/api/v2/"

  info "=== $name (Management API) ==="
  ensure_client "$name" non_interactive client_secret_post '["client_credentials"]' '{}' \
    || die "failed to ensure management client"
  mgmt_id="$ENSURED_CLIENT_ID"
  created="$ENSURED_CLIENT_CREATED"

  mgmt_secret="$(client_secret "$mgmt_id")"
  [ -n "$mgmt_secret" ] || die "failed to read management client secret"

  ensure_client_grant "$mgmt_id" "$audience" "$scopes_json" \
    || die "failed to ensure management client grant"

  {
    printf '# Builder API — Auth0 Management API M2M\n'
    printf 'AUTH0_MGMT_CLIENT_ID=%s\n' "$mgmt_id"
    printf 'AUTH0_MGMT_CLIENT_SECRET=%s\n\n' "$mgmt_secret"
  } >> "$OUTPUT"
  if [ "$created" = 1 ]; then
    info "management client: created ($mgmt_id)"
  else
    info "management client: exists ($mgmt_id)"
  fi
}

# --- main ------------------------------------------------------------------
RS_ID="$(jq -r '.resourceServer.identifier' "$APPS")"
RS_NAME="$(jq -r '.resourceServer.name' "$APPS")"
RS_ALG="$(jq -r '.resourceServer.signing_alg // "RS256"' "$APPS")"
RS_SCOPES="$(jq -c '.resourceServer.scopes' "$APPS")"
[ "$RS_ID" != "null" ] || die "apps.json: resourceServer.identifier is required"

ensure_resource_server "$RS_ID" "$RS_NAME" "$RS_ALG" "$RS_SCOPES"
ensure_device_flow "$RS_ID"

DOMAIN="$("$AUTH0_BIN" tenants list --json 2>/dev/null | jq -r '.[] | select(.active == true) | .name' | head -n1)"

{
  printf '# Generated by auth0-provisioner/provision/bootstrap.sh — contains secrets, do not commit\n\n'
  [ -n "$DOMAIN" ] && printf 'AUTH0_DOMAIN=%s\nAUTH0_ISSUER=https://%s/\nAUTH0_JWKS_URL=https://%s/.well-known/jwks.json\n\n' "$DOMAIN" "$DOMAIN" "$DOMAIN"
} > "$OUTPUT"

[ -n "$DOMAIN" ] && ensure_management_client "$DOMAIN"

NAPPS="$(jq '.apps | length' "$APPS")"
for i in $(seq 0 $((NAPPS - 1))); do
  APP="$(jq -c ".apps[$i]" "$APPS")"
  provision_app "$APP"
done

info "wrote $OUTPUT ($NAPPS app pair(s))"
