#!/usr/bin/env bash
#
# Bootstrap the OpenMeter/Konnect metering catalog for the clearinghouse collector.
#
# Idempotent: creates only what is missing. Meters are immutable in OpenMeter, so
# this script never updates or deletes them — it only adds missing meters/features.
# For customers it never mutates subject_keys on existing records (warns only).
#
# Requires: kongctl (https://developer.konghq.com/kongctl/) and jq.
# Auth:     KONGCTL_DEFAULT_KONNECT_PAT (preferred) or OPENMETER_API_KEY — a Konnect PAT (kpat_…).
# Endpoint: OPENMETER_URL (default https://us.api.konghq.com/v3/openmeter).
#
# Usage:
#   ./bootstrap.sh catalog
#       Ensure meters + features exist and the configured plan is present.
#
#   ./bootstrap.sh customer <client_id> <external_user_id> [display_name] [--subscribe]
#       Ensure an OpenMeter customer keyed <client_id>:<external_user_id> exists with
#       subject_keys = [<client_id>:<external_user_id>] (matches the CloudEvent subject).
#       M2M / managed users only. If external_user_id starts with owner:, delegates to
#       the owner command (bare {users.id} customer key).
#       --subscribe also ensures a subscription on the demo/M2M Starter plan (best-effort).
#
#   ./bootstrap.sh owner <user_id> [display_name] [--subscribe]
#       Ensure a shared app-owner customer keyed by bare {users.id} (CloudEvent subject
#       after the collector strips the owner: wire prefix from auth_id).
#       --subscribe uses the Owner Starter plan (network_spend + discounts.usage).
#
#   ./bootstrap.sh auth0-dcr
#       Ensure Auth0 API audience + M2M "Konnect Portal DCR Admin" + Management
#       API client grant (Auth0 CLI). Exports AUTH0_DCR_* for portal-dcr.
#
#   ./bootstrap.sh portal-dcr
#       Ensure Auth0 DCR provider + openid_connect application auth strategy on
#       Konnect (idempotent by name). Auto-runs auth0-dcr when credentials unset.
#
#   ./bootstrap.sh portal-publish
#       Create/find Dev Portal + usage API, upload OpenAPI, publish with the
#       Auth0 DCR auth strategy (runs portal-dcr if strategy missing).
#
#   ./bootstrap.sh all <client_id> <external_user_id> [display_name] [--subscribe]
#       catalog + customer in one run.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CATALOG="${CATALOG:-$SCRIPT_DIR/catalog.json}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }

# Trim leading/trailing whitespace (Bash 4+ parameter expansion).
trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

command -v kongctl >/dev/null 2>&1 || die "kongctl not found (https://developer.konghq.com/kongctl/)"
command -v jq >/dev/null 2>&1 || die "jq not found"
[ -f "$CATALOG" ] || die "catalog not found: $CATALOG"

# --- env file (repo-root .env) ---------------------------------------------
# Plain `source .env` does not export vars to child processes; load here so
# `./bootstrap.sh catalog` works without `set -a; source …`.
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/../../.env}"
if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

# --- auth + endpoint -------------------------------------------------------
PAT="${KONGCTL_DEFAULT_KONNECT_PAT:-${OPENMETER_API_KEY:-}}"
if [ -z "$PAT" ]; then
  die "no Konnect PAT — set KONGCTL_DEFAULT_KONNECT_PAT or OPENMETER_API_KEY in the environment or in $ENV_FILE"
fi
export KONGCTL_DEFAULT_KONNECT_PAT="$PAT"

if [ -z "${OPENMETER_URL:-}" ] && [ -n "${OPENMETER_INGEST_URL:-}" ]; then
  OPENMETER_URL="${OPENMETER_INGEST_URL%/events}"
fi
OPENMETER_URL="${OPENMETER_URL:-https://us.api.konghq.com/v3/openmeter}"
OPENMETER_URL="${OPENMETER_URL%/}"
BASE="$(printf '%s' "$OPENMETER_URL" | sed -E 's#(https?://[^/]+).*#\1#')"
PREFIX="$(printf '%s' "$OPENMETER_URL" | sed -E 's#https?://[^/]+##')"
[ -n "$PREFIX" ] || PREFIX="/v3/openmeter"

# --- kongctl api helpers (return response body as JSON on stdout) ----------
kapi_err() {
  local method="$1" path="$2" err="$3"
  die "kongctl api $method $path failed — check OPENMETER_URL ($OPENMETER_URL) and your PAT: $err"
}

kapi_warn() {
  local method="$1" path="$2" err="$3"
  warn "kongctl api $method $path failed — check OPENMETER_URL ($OPENMETER_URL) and your PAT: $err"
}

kapi_body_error() {
  printf '%s' "$1" | jq -r '.message // .detail // .title // empty'
}

kapi_body_is_error() {
  printf '%s' "$1" | jq -e 'type == "object" and (.message // .detail // .title // empty) != "" and (.data // .items // null) == null' >/dev/null 2>&1
}

kapi_run() {
  local mode="$1" method="$2" path="$3"
  shift 3
  local body err
  err="$(mktemp)"
  case "$method" in
    get)
      if ! body="$(kongctl api get "$PREFIX$path" --base-url "$BASE" -o json 2>"$err")"; then
        if [ "$mode" = soft ]; then kapi_warn get "$path" "$(cat "$err")"; rm -f "$err"; return 1; fi
        kapi_err get "$path" "$(cat "$err")"
      fi
      ;;
    post)
      if ! body="$(kongctl api post "$PREFIX$path" --base-url "$BASE" -o json -f - 2>"$err")"; then
        if [ "$mode" = soft ]; then kapi_warn post "$path" "$(cat "$err")"; rm -f "$err"; return 1; fi
        kapi_err post "$path" "$(cat "$err")"
      fi
      ;;
    put)
      if ! body="$(kongctl api put "$PREFIX$path" --base-url "$BASE" -o json -f - 2>"$err")"; then
        if [ "$mode" = soft ]; then kapi_warn put "$path" "$(cat "$err")"; rm -f "$err"; return 1; fi
        kapi_err put "$path" "$(cat "$err")"
      fi
      ;;
    delete)
      if ! body="$(kongctl api delete "$PREFIX$path" --base-url "$BASE" -o json 2>"$err")"; then
        if [ "$mode" = soft ]; then kapi_warn delete "$path" "$(cat "$err")"; rm -f "$err"; return 1; fi
        kapi_err delete "$path" "$(cat "$err")"
      fi
      ;;
    *) die "unknown kapi method: $method" ;;
  esac
  rm -f "$err"
  if [ -z "$body" ]; then
    if [ "$mode" = soft ]; then kapi_warn "$method" "$path" "empty response"; return 1; fi
    kapi_err "$method" "$path" "empty response"
  fi
  if kapi_body_is_error "$body"; then
    if [ "$mode" = soft ]; then kapi_warn "$method" "$path" "$(kapi_body_error "$body")"; return 1; fi
    kapi_err "$method" "$path" "$(kapi_body_error "$body")"
  fi
  printf '%s' "$body"
}

kapi_get()       { kapi_run hard get    "$1"; }
kapi_post()      { kapi_run hard post   "$1"; }
kapi_put()       { kapi_run hard put    "$1"; }
kapi_delete()    { kapi_run hard delete "$1"; }
kapi_get_soft()  { kapi_run soft get    "$1"; }
kapi_post_soft() { kapi_run soft post   "$1"; }
kapi_delete_soft() { kapi_run soft delete "$1"; }

# Catalog may use .plans[] (preferred) or a legacy single .plan.
catalog_plans_json() {
  jq -c 'if (.plans | type) == "array" and (.plans | length) > 0 then .plans
         elif .plan then [.plan]
         else [] end' "$CATALOG"
}

plan_key_for_role() {
  local role="$1"
  case "$role" in
    owner)
      if [ -n "${OPENMETER_OWNER_STARTER_PLAN_KEY:-}" ]; then
        printf '%s' "$OPENMETER_OWNER_STARTER_PLAN_KEY"
        return 0
      fi
      ;;
    m2m)
      if [ -n "${OPENMETER_DEMO_STARTER_PLAN_KEY:-}" ]; then
        printf '%s' "$OPENMETER_DEMO_STARTER_PLAN_KEY"
        return 0
      fi
      ;;
  esac
  catalog_plans_json | jq -r --arg r "$role" '
    (map(select(.subscribe_role == $r)) | .[0].key)
    // .[0].key
    // empty
  '
}

# Included cycle allowance override (USD micros). Empty = use catalog discounts.usage.
included_usd_micros_override() {
  local raw="${OPENMETER_DEFAULT_STARTER_INCLUDED_USD_MICROS:-}"
  if [ -n "$raw" ] && [[ "$raw" =~ ^[0-9]+$ ]] && [ "$raw" -gt 0 ]; then
    printf '%s' "$raw"
  fi
}

meter_id_for() {
  kapi_get /meters | jq -r --arg k "$1" '(.data // .)[] | select(.key == $k) | .id'
}

feature_for() {
  kapi_get /features | jq -c --arg k "$1" '(.data // .)[] | select(.key == $k)'
}

find_plan_by_status() {
  local plan_key="$1" status="$2"
  kapi_get "/plans?filter[key]=${plan_key}&filter[status]=${status}" \
    | jq -c --arg k "$plan_key" '(.data // .)[] | select(.key == $k)' | head -n 1
}

# --- catalog ---------------------------------------------------------------
ensure_meters() {
  local existing
  existing="$(kapi_get /meters | jq -r '(.data // .)[].key')"
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    local key; key="$(jq -r '.key' <<<"$m")"
    if printf '%s\n' "$existing" | grep -qxF "$key"; then
      info "meter   $key — exists"
      continue
    fi
    local body
    body="$(jq '{name, key, description, event_type, aggregation}
              + (if .value_property then {value_property} else {} end)
              + (if .dimensions     then {dimensions}     else {} end)' <<<"$m")"
    printf '%s' "$body" | kapi_post /meters >/dev/null
    info "meter   $key — created"
  done < <(jq -c '.meters[]' "$CATALOG")
}

feature_meter_key() { jq -r '.meter_key // .meter_slug // empty' <<<"$1"; }

create_feature() {
  local key="$1" name="$2" meter_id="$3"
  local body
  body="$(jq -n --arg key "$key" --arg name "$name" --arg mid "$meter_id" \
    '{key:$key, name:$name, meter:{id:$mid}}')"
  printf '%s' "$body" | kapi_post /features >/dev/null
}

ensure_features() {
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    local key meter_key meter_id feat_rec feat_id linked_meter body
    key="$(jq -r '.key' <<<"$f")"
    meter_key="$(feature_meter_key "$f")"
    [ -n "$meter_key" ] || die "feature $key requires meter_key in catalog.json"
    meter_id="$(meter_id_for "$meter_key")"
    [ -n "$meter_id" ] || die "meter $meter_key not found for feature $key"

    feat_rec="$(feature_for "$key")"
    if [ -z "$feat_rec" ]; then
      create_feature "$key" "$(jq -r '.name' <<<"$f")" "$meter_id"
      info "feature $key — created"
      continue
    fi

    feat_id="$(jq -r '.id' <<<"$feat_rec")"
    linked_meter="$(jq -r '.meter.id // empty' <<<"$feat_rec")"
    if [ "$linked_meter" = "$meter_id" ]; then
      info "feature $key — exists"
      continue
    fi

    warn "feature $key — exists without meter link; recreating"
    kapi_delete_soft "/features/$feat_id" >/dev/null 2>&1 || true
    create_feature "$key" "$(jq -r '.name' <<<"$f")" "$meter_id"
    info "feature $key — recreated (meter: $meter_key)"
  done < <(jq -c '.features[]' "$CATALOG")
}

build_plan_body() {
  local plan_json="$1" feat_map included
  feat_map="$(kapi_get /features | jq '[(.data // .)[] | {(.key): .id}] | add // {}')"
  included="$(included_usd_micros_override)"
  jq -n --argjson p "$plan_json" --argjson feats "$feat_map" --arg included "$included" '
    ($p) as $plan
    | {
        key: $plan.key,
        name: $plan.name,
        description: ($plan.description // empty),
        currency: $plan.currency,
        billing_cadence: $plan.billing_cadence,
        phases: [
          $plan.phases[]
          | {
              key,
              name,
              rate_cards: [
                .rate_cards[]
                | {
                    key,
                    name,
                    feature: { id: $feats[.feature_key] },
                    billing_cadence,
                    price
                  }
                  + (if $included != "" then { discounts: { usage: $included } }
                     elif (.discounts.usage // null) != null then { discounts }
                     else {} end)
              ]
            }
        ]
      }
    + (if ($plan.settlement_mode // "") != "" then { settlement_mode: $plan.settlement_mode } else {} end)
    + (if ($plan.metadata // null) != null then { metadata: $plan.metadata } else {} end)
    | if .description == "" then del(.description) else . end
  '
}

publish_plan() {
  local plan_id="$1" plan_key="$2" resp
  if resp="$(printf '{}' | kapi_post_soft "/plans/$plan_id/publish")" \
    && printf '%s' "$resp" | jq -e '.status == "active"' >/dev/null; then
    info "plan    $plan_key — published"
    return 0
  fi
  warn "plan    $plan_key — could not publish (ensure features have meter links)"
  return 1
}

ensure_one_plan() {
  local plan_json="$1"
  local plan_key
  plan_key="$(jq -r '.key // empty' <<<"$plan_json")"
  [ -n "$plan_key" ] || die "catalog plan entry missing key"

  if [ -n "$(find_plan_by_status "$plan_key" active)" ]; then
    info "plan    $plan_key — active"
    return 0
  fi

  local draft draft_id body plan_id
  draft="$(find_plan_by_status "$plan_key" draft)"
  if [ -n "$draft" ]; then
    draft_id="$(jq -r '.id' <<<"$draft")"
    info "plan    $plan_key — draft exists, publishing"
    publish_plan "$draft_id" "$plan_key" || true
    return 0
  fi

  body="$(build_plan_body "$plan_json")"
  if printf '%s' "$body" | jq -e '[
    .phases[].rate_cards[].feature.id
    | select(. == null or . == "")
  ] | length == 0' >/dev/null; then
    :
  else
    die "plan $plan_key rate cards reference unknown features — run ensure_features first"
  fi

  plan_id="$(printf '%s' "$body" | kapi_post /plans | jq -r '.id')"
  info "plan    $plan_key — created (draft)"
  publish_plan "$plan_id" "$plan_key" || true
}

ensure_plans() {
  local plans count
  plans="$(catalog_plans_json)"
  count="$(jq 'length' <<<"$plans")"
  if [ "$count" -eq 0 ]; then
    info "plan    — none configured"
    return 0
  fi
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    ensure_one_plan "$p"
  done < <(jq -c '.[]' <<<"$plans")
}

cmd_catalog() {
  info "== catalog ($BASE$PREFIX) =="
  ensure_meters
  ensure_features
  ensure_plans
}

# --- customer --------------------------------------------------------------
# Find a customer by exact key. NOTE: the list filter is a partial match, so we
# fetch and exact-match locally. For very large customer bases add pagination.
find_customer() {
  kapi_get "/customers" | jq -c --arg k "$1" '(.data // .)[] | select(.key == $k)' | head -n 1
}

# Create/ensure a customer with exact key + subject_keys = [key]. Never mutates
# subject_keys on existing records (OpenMeter forbids changes once subscribed).
# subscribe_role: owner | m2m — selects which Starter plan --subscribe uses.
ensure_customer_key() {
  local key="$1" display="$2" subscribe="$3" label="${4:-$1}" role="${5:-m2m}"
  [ -n "$key" ] || die "customer key is required"
  [ -n "$display" ] || display="$key"

  local cust; cust="$(find_customer "$key")"
  local id
  if [ -z "$cust" ]; then
    local body
    body="$(jq -n --arg key "$key" --arg name "$display" \
      '{key:$key, name:$name, usage_attribution:{subject_keys:[$key]}}')"
    id="$(printf '%s' "$body" | kapi_post /customers | jq -r '.id')"
    info "customer $label — created (subject: $key)"
  else
    id="$(jq -r '.id' <<<"$cust")"
    if jq -e --arg c "$key" '(.usage_attribution.subject_keys // []) | index($c)' <<<"$cust" >/dev/null; then
      info "customer $label — up to date"
    else
      warn "customer $label exists but its subject_keys do not include '$key'"
      warn "  (OpenMeter blocks subject_key changes on subscribed customers — reconcile manually)"
    fi
  fi

  [ "$subscribe" = "1" ] && ensure_subscription "$id" "$key" "$role" || true
}

ensure_owner_customer() {
  local user_id display="$2" subscribe="$3"
  user_id="$(trim "$1")"
  [ -n "$user_id" ] || die "owner requires <user_id>"
  # Strip accidental owner: prefix so the key is always the bare {users.id}.
  case "$user_id" in
    owner:*) user_id="$(trim "${user_id#owner:}")" ;;
  esac
  [ -n "$user_id" ] || die "owner requires a non-empty <user_id>"
  ensure_customer_key "$user_id" "$display" "$subscribe" "owner:$user_id" "owner"
}

ensure_customer() {
  local client_id external_user_id display="$3" subscribe="$4"
  client_id="$(trim "$1")"
  external_user_id="$(trim "$2")"
  [ -n "$client_id" ] && [ -n "$external_user_id" ] || die "customer requires <client_id> <external_user_id>"
  # App-owner wire subjects (owner:{users.id}) share one bare-id customer.
  case "$external_user_id" in
    owner:*)
      ensure_owner_customer "$external_user_id" "$display" "$subscribe"
      return
      ;;
  esac
  # M2M / managed user: CloudEvent subject = compound client_id:external_user_id.
  local compound="$client_id:$external_user_id"
  ensure_customer_key "$compound" "$display" "$subscribe" "$compound" "m2m"
}

# Best-effort subscription on the Starter plan for subscribe_role (owner|m2m).
# Skips if the customer already has any subscription.
ensure_subscription() {
  local customer_id="$1" label="$2" role="${3:-m2m}"
  local plan_key; plan_key="$(plan_key_for_role "$role")"
  [ -n "$plan_key" ] || { warn "no $role plan in catalog; skipping subscription"; return 0; }

  local existing resp
  existing="$(kapi_get_soft "/subscriptions?customer_id=$customer_id" 2>/dev/null \
    | jq -r --arg c "$customer_id" '(.data // .)[]? | select(.customer_id == $c) | .id' 2>/dev/null || true)"
  if [ -n "$existing" ]; then
    info "sub      $label — exists"
    return 0
  fi
  local body
  body="$(jq -n --arg ck "$label" --arg pk "$plan_key" \
    '{customer:{key:$ck}, plan:{key:$pk}}')"
  if resp="$(printf '%s' "$body" | kapi_post_soft /subscriptions)" && [ -n "$resp" ]; then
    info "sub      $label — created on $plan_key ($role)"
  else
    warn "sub      $label — could not create subscription on $plan_key (create manually if needed)"
  fi
}

# --- Auth0 DCR admin (Management API M2M) ----------------------------------
# Provisions the Auth0-side prerequisites from
# https://developer.konghq.com/how-to/auth0-dcr/ using the Auth0 CLI.
# Requires: auth0 login (interactive or machine credentials).

AUTH0_DCR_MGMT_SCOPES=(
  read:client_grants create:client_grants delete:client_grants update:client_grants
  read:clients create:clients delete:clients update:clients
  update:client_keys
)

auth0_domain() {
  local d
  d="$(trim "${AUTH0_DOMAIN:-}")"
  if [ -z "$d" ]; then
    d="$(trim "${AUTH0_DCR_ISSUER:-${AUTH0_ISSUER:-}}")"
    d="${d#https://}"
    d="${d#http://}"
    d="${d%%/*}"
  fi
  # Fall back to the Auth0 CLI's active tenant (auth0 login).
  if [ -z "$d" ] && command -v auth0 >/dev/null 2>&1; then
    d="$(auth0 tenants list --json-compact --no-input 2>/dev/null \
      | jq -r '(.[] | select(.active == true) | .name) // .[0].name // empty' | head -n 1)"
    d="$(trim "$d")"
  fi
  printf '%s' "$d"
}

auth0_issuer_url() {
  local d; d="$(auth0_domain)"
  [ -n "$d" ] || die "AUTH0_DOMAIN or AUTH0_DCR_ISSUER / AUTH0_ISSUER is required"
  printf 'https://%s' "$d"
}

auth0_mgmt_audience() {
  printf '%s/api/v2/' "$(auth0_issuer_url)"
}

ensure_auth0_api_audience() {
  local audience="$1" name="${2:-Livepeer Clearinghouse}"
  local existing
  existing="$(auth0 apis list --json-compact --no-input 2>/dev/null \
    | jq -c --arg id "$audience" '.[] | select(.identifier == $id)' | head -n 1)"
  if [ -n "$existing" ]; then
    info "api     $audience — exists"
    return 0
  fi
  auth0 apis create \
    --name "$name" \
    --identifier "$audience" \
    --offline-access=false \
    --signing-alg RS256 \
    --no-input --json-compact >/dev/null
  info "api     $audience — created"
}

find_auth0_app_by_name() {
  local name="$1"
  auth0 apps list --json-compact --no-input 2>/dev/null \
    | jq -c --arg n "$name" '.[] | select(.name == $n)' | head -n 1
}

ensure_auth0_dcr_mgmt_grant() {
  local client_id="$1"
  local audience; audience="$(auth0_mgmt_audience)"
  local scopes_csv grants grant_id missing payload
  scopes_csv="$(IFS=,; echo "${AUTH0_DCR_MGMT_SCOPES[*]}")"
  if [ "${AUTH0_DCR_USE_DEVELOPER_MANAGED_SCOPES:-0}" = "1" ]; then
    scopes_csv="${scopes_csv},read:resource_servers"
  fi

  grants="$(auth0 api get client-grants --no-input 2>/dev/null || true)"
  grant_id="$(printf '%s' "$grants" | jq -r --arg c "$client_id" --arg a "$audience" '
    .[] | select(.client_id == $c and .audience == $a) | .id' | head -n 1)"

  if [ -n "$grant_id" ]; then
    missing="$(printf '%s' "$grants" | jq -r --arg id "$grant_id" --arg csv "$scopes_csv" '
      ($csv | split(",") | map(gsub("^\\s+|\\s+$";""))) as $need
      | (.[] | select(.id == $id) | .scope) as $have
      | ($need - $have) | .[]
    ')"
    if [ -z "$missing" ]; then
      info "grant   Management API — exists for $client_id"
      return 0
    fi
    # Replace scope list with union of existing + required.
    payload="$(printf '%s' "$grants" | jq -c --arg id "$grant_id" --arg csv "$scopes_csv" '
      ($csv | split(",") | map(gsub("^\\s+|\\s+$";""))) as $need
      | (.[] | select(.id == $id) | .scope) as $have
      | {scope: (($have + $need) | unique)}
    ')"
    auth0 api patch "client-grants/$grant_id" --data "$payload" --no-input >/dev/null
    info "grant   Management API — updated scopes for $client_id"
    return 0
  fi

  payload="$(jq -n --arg c "$client_id" --arg a "$audience" --arg csv "$scopes_csv" '
    {
      client_id: $c,
      audience: $a,
      scope: ($csv | split(",") | map(gsub("^\\s+|\\s+$";"")))
    }')"
  auth0 api post client-grants --data "$payload" --no-input >/dev/null
  info "grant   Management API — created for $client_id"
}

# Sets AUTH0_DCR_CLIENT_ID / AUTH0_DCR_CLIENT_SECRET / AUTH0_DCR_ISSUER in the
# current shell environment.
#
# Demo default: reuse DEMO_APP_AUTH0_M2M_* (one M2M for signer mint + Konnect DCR)
# when those are set. Set AUTH0_DCR_DEDICATED=1 to force a separate
# "Konnect Portal DCR Admin" app (least privilege for production).
cmd_auth0_dcr() {
  command -v auth0 >/dev/null 2>&1 || die "auth0 CLI not found (brew install auth0/auth0-cli/auth0)"
  command -v jq >/dev/null 2>&1 || die "jq not found"

  local app_name audience issuer domain client_id client_secret app created
  local demo_id demo_secret dedicated
  audience="$(trim "${AUTH0_DCR_AUDIENCE:-${AUTH0_AUDIENCE:-${DEMO_APP_AUTH0_AUDIENCE:-livepeer-clearinghouse}}}")"
  issuer="$(auth0_issuer_url)"
  domain="$(auth0_domain)"
  demo_id="$(trim "${DEMO_APP_AUTH0_M2M_CLIENT_ID:-${AUTH0_SIGNER_M2M_CLIENT_ID:-}}")"
  demo_secret="$(trim "${DEMO_APP_AUTH0_M2M_CLIENT_SECRET:-${AUTH0_SIGNER_M2M_CLIENT_SECRET:-}}")"
  dedicated="${AUTH0_DCR_DEDICATED:-0}"
  app_name="$(trim "${AUTH0_DCR_APP_NAME:-Konnect Portal DCR Admin}")"

  info "== auth0-dcr ($domain) =="
  info "audience $audience  (portal / Gateway token audience)"
  info "mgmt     $(auth0_mgmt_audience)  (DCR admin client grant)"

  ensure_auth0_api_audience "$audience" "Livepeer Clearinghouse"

  if [ "$dedicated" != "1" ] && [ -n "$demo_id" ] && [ -n "$demo_secret" ]; then
    client_id="$demo_id"
    client_secret="$demo_secret"
    info "m2m     reusing Demo/signer M2M ($client_id) for DCR admin"
    info "         (set AUTH0_DCR_DEDICATED=1 for a separate least-privilege app)"
  else
    app="$(find_auth0_app_by_name "$app_name" || true)"
    if [ -n "$app" ]; then
      client_id="$(jq -r '.client_id // empty' <<<"$app")"
      [ -n "$client_id" ] || die "existing Auth0 app '$app_name' has no client_id"
      client_secret="$(auth0 apps show "$client_id" -r --json-compact --no-input \
        | jq -r '.client_secret // empty')"
      [ -n "$client_secret" ] || die "could not reveal client_secret for $client_id (auth0 apps show -r)"
      info "m2m     $app_name — exists ($client_id)"
    else
      created="$(auth0 apps create \
        --name "$app_name" \
        --description "Konnect Dev Portal Dynamic Client Registration admin" \
        --type m2m \
        --reveal-secrets \
        --no-input --json-compact)"
      client_id="$(jq -r '.client_id // empty' <<<"$created")"
      client_secret="$(jq -r '.client_secret // empty' <<<"$created")"
      [ -n "$client_id" ] && [ -n "$client_secret" ] \
        || die "auth0 apps create did not return client_id/secret: $created"
      info "m2m     $app_name — created ($client_id)"
    fi
  fi

  ensure_auth0_dcr_mgmt_grant "$client_id"

  export AUTH0_DCR_CLIENT_ID="$client_id"
  export AUTH0_DCR_CLIENT_SECRET="$client_secret"
  export AUTH0_DCR_ISSUER="$issuer"
  export AUTH0_DCR_AUDIENCE="$audience"
  if [ -z "${AUTH0_DCR_INITIAL_CLIENT_AUDIENCE:-}" ] && [ "${AUTH0_DCR_SET_INITIAL_AUDIENCE:-0}" = "1" ]; then
    export AUTH0_DCR_INITIAL_CLIENT_AUDIENCE
    AUTH0_DCR_INITIAL_CLIENT_AUDIENCE="$(auth0_mgmt_audience)"
  fi

  info ""
  info "DCR admin = $client_id (same secret as DEMO_APP_AUTH0_M2M when reused)"
  info "  AUTH0_DCR_ISSUER=$issuer"
  info "  AUTH0_DCR_AUDIENCE=$audience"
  if [ "${AUTH0_DCR_WRITE_ENV:-0}" = "1" ] && [ -f "$ENV_FILE" ]; then
    # Only write issuer/audience aliases — client id/secret already live in DEMO_APP_* when reused.
    if ! grep -q '^AUTH0_DCR_ISSUER=' "$ENV_FILE" 2>/dev/null; then
      {
        echo ""
        echo "# Auth0 DCR (bootstrap.sh auth0-dcr $(date -u +%Y-%m-%dT%H:%MZ))"
        echo "AUTH0_DCR_ISSUER=$issuer"
        echo "AUTH0_DCR_AUDIENCE=$audience"
      } >>"$ENV_FILE"
      info "wrote   AUTH0_DCR_ISSUER/AUDIENCE to $ENV_FILE"
    fi
  fi
}

# --- Dev Portal Auth0 DCR (Konnect application auth strategy) --------------
# Creates the DCR provider + openid_connect strategy used by Dev Portal /
# Gateway OIDC. Optionally provisions the Auth0 M2M first (auth0-dcr).
#
# Docs: https://developer.konghq.com/how-to/auth0-dcr/
# UI:   https://cloud.konghq.com/us/portals/application-auth/auth-strategy/create

konnect_curl() {
  local method="$1" path="$2"
  shift 2
  local url="${KONNECT_API_BASE}${path}"
  local code body tmp
  tmp="$(mktemp)"
  code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$url" \
    -H "Authorization: Bearer $PAT" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    "$@")"
  body="$(cat "$tmp")"
  rm -f "$tmp"
  if [ "$code" -lt 200 ] || [ "$code" -ge 300 ]; then
    printf '%s\n' "$body" >&2
    die "Konnect $method $path → HTTP $code"
  fi
  printf '%s' "$body"
}

konnect_get_soft() {
  local path="$1"
  local url="${KONNECT_API_BASE}${path}"
  local code body tmp
  tmp="$(mktemp)"
  code="$(curl -sS -o "$tmp" -w '%{http_code}' -X GET "$url" \
    -H "Authorization: Bearer $PAT" \
    -H "Accept: application/json")" || true
  body="$(cat "$tmp")"
  rm -f "$tmp"
  if [ "$code" = "200" ]; then
    printf '%s' "$body"
    return 0
  fi
  return 1
}

find_dcr_provider_by_name() {
  local name="$1" body
  body="$(konnect_get_soft /v2/dcr-providers)" || return 1
  printf '%s' "$body" | jq -c --arg n "$name" '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.name == $n))
    | .[0] // empty
  '
}

find_auth_strategy_by_name() {
  local name="$1" body
  body="$(konnect_get_soft /v2/application-auth-strategies)" || return 1
  printf '%s' "$body" | jq -c --arg n "$name" '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.name == $n))
    | .[0] // empty
  '
}

cmd_portal_dcr() {
  command -v curl >/dev/null 2>&1 || die "curl not found"

  KONNECT_API_BASE="${KONNECT_API_BASE:-$BASE}"
  KONNECT_API_BASE="${KONNECT_API_BASE%/}"

  local issuer client_id client_secret audience
  local provider_name strategy_name strategy_display
  local initial_audience use_dev_scopes

  # Prefer explicit AUTH0_DCR_*; else Demo/signer M2M; else provision via auth0-dcr.
  client_id="$(trim "${AUTH0_DCR_CLIENT_ID:-${DEMO_APP_AUTH0_M2M_CLIENT_ID:-${AUTH0_SIGNER_M2M_CLIENT_ID:-}}}")"
  client_secret="$(trim "${AUTH0_DCR_CLIENT_SECRET:-${DEMO_APP_AUTH0_M2M_CLIENT_SECRET:-${AUTH0_SIGNER_M2M_CLIENT_SECRET:-}}}")"
  if [ -z "$client_id" ] || [ -z "$client_secret" ] || [ "${AUTH0_DCR_PROVISION_AUTH0:-0}" = "1" ]; then
    if command -v auth0 >/dev/null 2>&1; then
      cmd_auth0_dcr
      client_id="$(trim "${AUTH0_DCR_CLIENT_ID:-}")"
      client_secret="$(trim "${AUTH0_DCR_CLIENT_SECRET:-}")"
    elif [ -z "$client_id" ] || [ -z "$client_secret" ]; then
      die "AUTH0_DCR_CLIENT_ID/SECRET unset — set DEMO_APP_AUTH0_M2M_* or run ./bootstrap.sh auth0-dcr"
    fi
  fi
  # Ensure Management API grant even when reusing Demo M2M without re-running auth0-dcr create.
  if command -v auth0 >/dev/null 2>&1 && [ -n "$client_id" ]; then
    ensure_auth0_dcr_mgmt_grant "$client_id" || true
  fi

  issuer="$(trim "${AUTH0_DCR_ISSUER:-${AUTH0_ISSUER:-}}")"
  issuer="${issuer%/}"
  issuer="${issuer%/authorize}"

  audience="$(trim "${AUTH0_DCR_AUDIENCE:-${AUTH0_AUDIENCE:-livepeer-clearinghouse}}")"
  provider_name="$(trim "${AUTH0_DCR_PROVIDER_NAME:-Auth0 Production}")"
  strategy_name="$(trim "${AUTH0_DCR_STRATEGY_NAME:-Auth0 DCR Auth Strategy}")"
  strategy_display="$(trim "${AUTH0_DCR_STRATEGY_DISPLAY_NAME:-$strategy_name}")"
  initial_audience="$(trim "${AUTH0_DCR_INITIAL_CLIENT_AUDIENCE:-}")"
  use_dev_scopes="${AUTH0_DCR_USE_DEVELOPER_MANAGED_SCOPES:-0}"

  [ -n "$issuer" ] || die "AUTH0_DCR_ISSUER (or AUTH0_ISSUER) is required — e.g. https://YOUR_TENANT.us.auth0.com"
  [ -n "$client_id" ] || die "AUTH0_DCR_CLIENT_ID is required (Auth0 M2M 'Konnect Portal DCR Admin')"
  [ -n "$client_secret" ] || die "AUTH0_DCR_CLIENT_SECRET is required"
  [ -n "$audience" ] || die "AUTH0_DCR_AUDIENCE is required (API identifier, e.g. livepeer-clearinghouse)"

  info "== portal-dcr ($KONNECT_API_BASE) =="
  info "issuer   $issuer"
  info "audience $audience  (token audience for portal apps / Gateway OIDC)"
  info "provider $provider_name  (provider_type=auth0 — immutable after create)"

  local existing_provider provider_id dcr_config body managed_json
  if [ "$use_dev_scopes" = "1" ]; then managed_json=true; else managed_json=false; fi
  existing_provider="$(find_dcr_provider_by_name "$provider_name" || true)"
  if [ -n "$existing_provider" ]; then
    provider_id="$(jq -r '.id // empty' <<<"$existing_provider")"
    info "dcr     $provider_name — exists ($provider_id)"
  else
    dcr_config="$(jq -n \
      --arg id "$client_id" \
      --arg secret "$client_secret" \
      --arg aud "$initial_audience" \
      --argjson managed "$managed_json" \
      '{
         initial_client_id: $id,
         initial_client_secret: $secret
       }
       + (if $aud != "" then {initial_client_audience: $aud} else {} end)
       + (if $managed then {use_developer_managed_scopes: true} else {} end)')"
    body="$(jq -n \
      --arg name "$provider_name" \
      --arg issuer "$issuer" \
      --argjson cfg "$dcr_config" \
      '{
         name: $name,
         provider_type: "auth0",
         issuer: $issuer,
         dcr_config: $cfg
       }')"
    existing_provider="$(printf '%s' "$body" | konnect_curl POST /v2/dcr-providers -d @-)"
    provider_id="$(jq -r '.id // empty' <<<"$existing_provider")"
    [ -n "$provider_id" ] || die "DCR provider create returned no id: $existing_provider"
    info "dcr     $provider_name — created ($provider_id)"
  fi

  local existing_strategy strategy_id scopes_json scopes_csv
  scopes_csv="$(trim "${AUTH0_DCR_SCOPES:-sign:mint_user_token,sign:job}")"
  scopes_json="$(printf '%s' "$scopes_csv" | jq -Rc 'split(",") | map(gsub("^\\s+|\\s+$";"")) | map(select(length>0))')"
  existing_strategy="$(find_auth_strategy_by_name "$strategy_name" || true)"
  if [ -n "$existing_strategy" ]; then
    strategy_id="$(jq -r '.id // empty' <<<"$existing_strategy")"
    [ -n "$strategy_id" ] || die "auth strategy exists but has no id"
    # Sync scopes on existing strategies (create path is below). Konnect may reject
    # a full replace of immutable fields — PATCH configs only.
    body="$(jq -n \
      --arg issuer "$issuer" \
      --arg aud "$audience" \
      --argjson scopes "$scopes_json" \
      '{
         configs: {
           "openid-connect": {
             issuer: $issuer,
             credential_claim: ["azp"],
             scopes: $scopes,
             auth_methods: ["client_credentials", "bearer"],
             token_post_args_names: ["audience"],
             token_post_args_values: [$aud]
           }
         }
       }')"
    printf '%s' "$body" | konnect_curl PATCH "/v2/application-auth-strategies/${strategy_id}" -d @- >/dev/null
    info "auth    $strategy_name — exists ($strategy_id); scopes=[$scopes_csv]"
  else
    body="$(jq -n \
      --arg name "$strategy_name" \
      --arg display "$strategy_display" \
      --arg issuer "$issuer" \
      --arg aud "$audience" \
      --arg provider "$provider_id" \
      --argjson scopes "$scopes_json" \
      '{
         name: $name,
         display_name: $display,
         strategy_type: "openid_connect",
         dcr_provider_id: $provider,
         configs: {
           "openid-connect": {
             issuer: $issuer,
             credential_claim: ["azp"],
             scopes: $scopes,
             auth_methods: ["client_credentials", "bearer"],
             token_post_args_names: ["audience"],
             token_post_args_values: [$aud]
           }
         }
       }')"
    existing_strategy="$(printf '%s' "$body" | konnect_curl POST /v2/application-auth-strategies -d @-)"
    strategy_id="$(jq -r '.id // empty' <<<"$existing_strategy")"
    [ -n "$strategy_id" ] || die "auth strategy create returned no id: $existing_strategy"
    info "auth    $strategy_name — created ($strategy_id); scopes=[$scopes_csv]"
  fi

  info ""
  info "Next: ./bootstrap.sh portal-publish   # portal + API + publish with this strategy"
  info "  AUTH_STRATEGY_ID=$strategy_id  DCR_PROVIDER_ID=$provider_id"
  export AUTH_STRATEGY_ID="$strategy_id"
  export DCR_PROVIDER_ID="$provider_id"
}

# --- Dev Portal publish (API + auth strategy) ------------------------------
# Creates portal + page + catalog API (OpenAPI) and publishes with the Auth0
# DCR application auth strategy. Idempotent by portal/API name.
# Docs: https://developer.konghq.com/how-to/automate-api-catalog/

find_portal_by_name() {
  local name="$1" body
  body="$(konnect_get_soft /v3/portals)" || body="$(konnect_get_soft /v2/portals)" || return 1
  printf '%s' "$body" | jq -c --arg n "$name" '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.name == $n))
    | .[0] // empty
  '
}

find_api_by_name() {
  local name="$1" body
  body="$(konnect_get_soft /v3/apis)" || return 1
  printf '%s' "$body" | jq -c --arg n "$name" '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.name == $n))
    | .[0] // empty
  '
}

ensure_portal_home_page() {
  local portal_id="$1" pages body
  pages="$(konnect_get_soft "/v3/portals/${portal_id}/pages")" || true
  if printf '%s' "$pages" | jq -e '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.slug == "/" or .slug == "/apis"))
    | length > 0
  ' >/dev/null 2>&1; then
    info "page    / — exists"
    return 0
  fi
  body="$(jq -n '{
    title: "Clearinghouse APIs",
    slug: "/",
    visibility: "public",
    status: "published",
    content: "# Livepeer Clearinghouse\n\nSelf-serve usage for your app.\n\n::apis-list\n---\npersist-page-number: true\ncta-text: \"View APIs\"\n---\n"
  }')"
  printf '%s' "$body" | konnect_curl POST "/v3/portals/${portal_id}/pages" -d @- >/dev/null
  info "page    / — created"
}

ensure_api_openapi_version() {
  local api_id="$1"
  local openapi_path="$2"
  local version="${3:-1.0.0}"
  local versions content body existing_id
  [ -f "$openapi_path" ] || die "OpenAPI not found: $openapi_path"

  content="$(jq -c --arg v "$version" '.info.version = $v' "$openapi_path")"
  body="$(jq -n --arg v "$version" --arg content "$content" \
    '{version: $v, spec: {content: $content}}')"

  versions="$(konnect_get_soft "/v3/apis/${api_id}/versions")" || true
  existing_id="$(printf '%s' "$versions" | jq -r --arg v "$version" '
    (.data // .items // .)
    | if type == "array" then . else [] end
    | map(select(.version == $v))
    | .[0].id // empty
  ')"

  if [ -n "$existing_id" ]; then
    # Konnect allows PATCH (not PUT) to refresh spec content in place.
    printf '%s' "$(jq -n --arg content "$content" '{spec: {content: $content}}')" \
      | konnect_curl PATCH "/v3/apis/${api_id}/versions/${existing_id}" -d @- >/dev/null
    info "spec    $version — updated from $(basename "$openapi_path")"
    return 0
  fi

  printf '%s' "$body" | konnect_curl POST "/v3/apis/${api_id}/versions" -d @- >/dev/null
  info "spec    $version — uploaded from $(basename "$openapi_path")"
}

cmd_portal_publish() {
  command -v curl >/dev/null 2>&1 || die "curl not found"

  KONNECT_API_BASE="${KONNECT_API_BASE:-$BASE}"
  KONNECT_API_BASE="${KONNECT_API_BASE%/}"

  local portal_name api_name strategy_name strategy_id
  local portal_id api_id openapi_path visibility auto_approve body
  local existing_portal existing_api existing_strategy

  portal_name="$(trim "${KONNECT_PORTAL_NAME:-clearinghouse}")"
  api_name="$(trim "${KONNECT_API_NAME:-clearinghouse-usage}")"
  strategy_name="$(trim "${AUTH0_DCR_STRATEGY_NAME:-Auth0 DCR Auth Strategy}")"
  strategy_id="$(trim "${AUTH_STRATEGY_ID:-${KONNECT_AUTH_STRATEGY_ID:-}}")"
  visibility="$(trim "${KONNECT_API_VISIBILITY:-public}")"
  auto_approve="${KONNECT_AUTO_APPROVE_REGISTRATIONS:-true}"
  openapi_path="${KONNECT_OPENAPI_PATH:-$SCRIPT_DIR/../builder-api/cmd/builder-api/openapi.json}"

  info "== portal-publish ($KONNECT_API_BASE) =="

  if [ -z "$strategy_id" ]; then
    existing_strategy="$(find_auth_strategy_by_name "$strategy_name" || true)"
    strategy_id="$(jq -r '.id // empty' <<<"${existing_strategy:-}")"
  fi
  if [ -z "$strategy_id" ]; then
    info "auth strategy missing — running portal-dcr first"
    cmd_portal_dcr
    strategy_id="$(trim "${AUTH_STRATEGY_ID:-}")"
    if [ -z "$strategy_id" ]; then
      existing_strategy="$(find_auth_strategy_by_name "$strategy_name" || true)"
      strategy_id="$(jq -r '.id // empty' <<<"${existing_strategy:-}")"
    fi
  fi
  [ -n "$strategy_id" ] || die "no auth strategy id — run ./bootstrap.sh portal-dcr first"
  info "auth    $strategy_name ($strategy_id)"

  # Prefer explicit IDs; else find/create by name.
  portal_id="$(trim "${KONNECT_PORTAL_ID:-}")"
  if [ -z "$portal_id" ]; then
    existing_portal="$(find_portal_by_name "$portal_name" || true)"
    portal_id="$(jq -r '.id // empty' <<<"${existing_portal:-}")"
  fi
  if [ -z "$portal_id" ]; then
    body="$(jq -n --arg name "$portal_name" --arg sid "$strategy_id" '{
      name: $name,
      display_name: "Livepeer Clearinghouse",
      description: "JWT-scoped usage API for clearinghouse integrators",
      authentication_enabled: true,
      auto_approve_applications: true,
      auto_approve_developers: true,
      default_api_visibility: "public",
      default_page_visibility: "public",
      default_application_auth_strategy_id: $sid
    }')"
    existing_portal="$(printf '%s' "$body" | konnect_curl POST /v3/portals -d @-)"
    portal_id="$(jq -r '.id // empty' <<<"$existing_portal")"
    [ -n "$portal_id" ] || die "portal create returned no id: $existing_portal"
    info "portal  $portal_name — created ($portal_id)"
  else
    info "portal  $portal_name — exists ($portal_id)"
  fi

  ensure_portal_home_page "$portal_id"

  api_id="$(trim "${KONNECT_API_ID:-}")"
  if [ -z "$api_id" ]; then
    existing_api="$(find_api_by_name "$api_name" || true)"
    api_id="$(jq -r '.id // empty' <<<"${existing_api:-}")"
  fi
  if [ -z "$api_id" ]; then
    body="$(jq -n --arg name "$api_name" '{
      name: $name,
      description: "GET /api/v1/users/me/usage and /users/me/balance — signer-JWT OpenMeter reads"
    }')"
    existing_api="$(printf '%s' "$body" | konnect_curl POST /v3/apis -d @-)"
    api_id="$(jq -r '.id // empty' <<<"$existing_api")"
    [ -n "$api_id" ] || die "API create returned no id: $existing_api"
    info "api     $api_name — created ($api_id)"
  else
    info "api     $api_name — exists ($api_id)"
  fi

  ensure_api_openapi_version "$api_id" "$openapi_path" "${KONNECT_API_VERSION:-1.0.0}"

  # Optional: link Gateway Service implementation when IDs are provided.
  if [ -n "${KONNECT_CONTROL_PLANE_ID:-}" ] && [ -n "${KONNECT_SERVICE_ID:-}" ]; then
    local impls
    impls="$(konnect_get_soft "/v3/apis/${api_id}/implementations")" || true
    if printf '%s' "$impls" | jq -e --arg s "$KONNECT_SERVICE_ID" '
      (.data // .items // .)
      | if type == "array" then . else [] end
      | map(select(.service.id == $s))
      | length > 0
    ' >/dev/null 2>&1; then
      info "impl    service $KONNECT_SERVICE_ID — linked"
    else
      body="$(jq -n --arg cp "$KONNECT_CONTROL_PLANE_ID" --arg sid "$KONNECT_SERVICE_ID" \
        '{service: {control_plane_id: $cp, id: $sid}}')"
      printf '%s' "$body" | konnect_curl POST "/v3/apis/${api_id}/implementations" -d @- >/dev/null
      info "impl    service $KONNECT_SERVICE_ID — linked"
    fi
  else
    info "impl    skipped (set KONNECT_CONTROL_PLANE_ID + KONNECT_SERVICE_ID to link Gateway)"
  fi

  body="$(jq -n \
    --arg sid "$strategy_id" \
    --arg vis "$visibility" \
    --argjson auto "$( [ "$auto_approve" = "true" ] || [ "$auto_approve" = "1" ] && echo true || echo false )" \
    '{
      visibility: $vis,
      auto_approve_registrations: $auto,
      auth_strategy_ids: [$sid]
    }')"
  printf '%s' "$body" | konnect_curl PUT \
    "/v3/apis/${api_id}/publications/${portal_id}" -d @- >/dev/null
  info "publish — ok (auth_strategy_ids=[$strategy_id])"

  export KONNECT_PORTAL_ID="$portal_id"
  export KONNECT_API_ID="$api_id"
  export AUTH_STRATEGY_ID="$strategy_id"

  info ""
  info "Portal ID:   $portal_id"
  info "API ID:      $api_id"
  info "Strategy ID: $strategy_id"
  info "Open: https://cloud.konghq.com/us/portals/$portal_id"
}

# --- arg parsing -----------------------------------------------------------
SUBSCRIBE=0
ARGS=()
for a in "$@"; do
  case "$a" in
    --subscribe) SUBSCRIBE=1 ;;
    *) ARGS+=("$a") ;;
  esac
done
if [ "${#ARGS[@]}" -eq 0 ]; then set -- catalog; else set -- "${ARGS[@]}"; fi

cmd="${1:-catalog}"; shift || true
case "$cmd" in
  catalog)
    cmd_catalog
    ;;
  customer)
    ensure_customer "${1:-}" "${2:-}" "${3:-}" "$SUBSCRIBE"
    ;;
  owner)
    ensure_owner_customer "${1:-}" "${2:-}" "$SUBSCRIBE"
    ;;
  auth0-dcr)
    cmd_auth0_dcr
    ;;
  portal-dcr)
    cmd_portal_dcr
    ;;
  portal-publish)
    cmd_portal_publish
    ;;
  all)
    cmd_catalog
    ensure_customer "${1:-}" "${2:-}" "${3:-}" "$SUBSCRIBE"
    ;;
  *)
    die "unknown command '$cmd' (expected: catalog | customer | owner | auth0-dcr | portal-dcr | portal-publish | all)"
    ;;
esac
info "done."
