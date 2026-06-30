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
#       --subscribe also ensures a subscription on the catalog plan (best-effort).
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

plan_config_key() { jq -r '.plan.key // .plan_key // empty' "$CATALOG"; }

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
  local feat_map
  feat_map="$(kapi_get /features | jq '[(.data // .)[] | {(.key): .id}] | add')"
  jq --argjson feats "$feat_map" '
    .plan as $p
    | {
        key: $p.key,
        name: $p.name,
        description: ($p.description // empty),
        currency: $p.currency,
        billing_cadence: $p.billing_cadence,
        phases: [
          $p.phases[]
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
              ]
            }
        ]
      }
    | if .description == "" then del(.description) else . end
  ' "$CATALOG"
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

ensure_plan() {
  local plan_key
  plan_key="$(plan_config_key)"
  [ -n "$plan_key" ] || { info "plan    — none configured"; return 0; }
  jq -e '.plan' "$CATALOG" >/dev/null 2>&1 \
    || die "catalog plan block missing — add .plan or remove plan_key"

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

  body="$(build_plan_body)"
  if printf '%s' "$body" | jq -e '[
    .phases[].rate_cards[].feature.id
    | select(. == null or . == "")
  ] | length == 0' >/dev/null; then
    :
  else
    die "plan rate cards reference unknown features — run ensure_features first"
  fi

  plan_id="$(printf '%s' "$body" | kapi_post /plans | jq -r '.id')"
  info "plan    $plan_key — created (draft)"
  publish_plan "$plan_id" "$plan_key" || true
}

cmd_catalog() {
  info "== catalog ($BASE$PREFIX) =="
  ensure_meters
  ensure_features
  ensure_plan
}

# --- customer --------------------------------------------------------------
# Find a customer by exact key. NOTE: the list filter is a partial match, so we
# fetch and exact-match locally. For very large customer bases add pagination.
find_customer() {
  kapi_get "/customers" | jq -c --arg k "$1" '(.data // .)[] | select(.key == $k)' | head -n 1
}

ensure_customer() {
  local client_id="$1" external_user_id="$2" display="$3" subscribe="$4"
  [ -n "$client_id" ] && [ -n "$external_user_id" ] || die "customer requires <client_id> <external_user_id>"
  # The CloudEvent subject is the compound client_id:external_user_id (globally
  # unique, tenant-scoped). It is also the customer key and its only subject_key.
  # OpenMeter forbids changing subject_keys once a customer has an active
  # subscription, so we set it correctly at creation and never mutate it.
  local compound="$client_id:$external_user_id"
  [ -n "$display" ] || display="$compound"

  local cust; cust="$(find_customer "$compound")"
  local id
  if [ -z "$cust" ]; then
    local body
    body="$(jq -n --arg key "$compound" --arg name "$display" \
      '{key:$key, name:$name, usage_attribution:{subject_keys:[$key]}}')"
    id="$(printf '%s' "$body" | kapi_post /customers | jq -r '.id')"
    info "customer $compound — created (subject: $compound)"
  else
    id="$(jq -r '.id' <<<"$cust")"
    if jq -e --arg c "$compound" '(.usage_attribution.subject_keys // []) | index($c)' <<<"$cust" >/dev/null; then
      info "customer $compound — up to date"
    else
      warn "customer $compound exists but its subject_keys do not include '$compound'"
      warn "  (OpenMeter blocks subject_key changes on subscribed customers — reconcile manually)"
    fi
  fi

  [ "$subscribe" = "1" ] && ensure_subscription "$id" "$compound" || true
}

# Best-effort subscription on the catalog plan. Skips if the customer already has one.
ensure_subscription() {
  local customer_id="$1" label="$2"
  local plan_key; plan_key="$(plan_config_key)"
  [ -n "$plan_key" ] || { warn "no plan_key in catalog; skipping subscription"; return 0; }

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
    info "sub      $label — created on $plan_key"
  else
    warn "sub      $label — could not create subscription on $plan_key (create manually if needed)"
  fi
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
  all)
    cmd_catalog
    ensure_customer "${1:-}" "${2:-}" "${3:-}" "$SUBSCRIBE"
    ;;
  *)
    die "unknown command '$cmd' (expected: catalog | customer | all)"
    ;;
esac
info "done."
