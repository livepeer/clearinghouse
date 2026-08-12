#!/usr/bin/env bash
# Idempotent: create/deploy/bind the Credentials Exchange Action for signer JWT claims.
# Requires: auth0 login session (same as bootstrap.sh).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACTION_NAME="Clearinghouse Signer Claims"
ACTION_CODE_FILE="${ACTION_CODE_FILE:-$SCRIPT_DIR/credentials-exchange-action.js}"
TRIGGER="credentials-exchange"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*" >&2; }

[ -f "$ACTION_CODE_FILE" ] || die "action code not found: $ACTION_CODE_FILE"

AUTH0_BIN="${AUTH0_BIN:-auth0}"
command -v "$AUTH0_BIN" >/dev/null 2>&1 || AUTH0_BIN="$SCRIPT_DIR/auth0"
[ -x "$AUTH0_BIN" ] || die "auth0 CLI required (run bootstrap.sh or install auth0 CLI)"

ACTION_ID="$("$AUTH0_BIN" actions list --json 2>/dev/null | jq -r --arg n "$ACTION_NAME" '.[] | select(.name == $n) | .id' | head -n1)"

if [ -z "$ACTION_ID" ]; then
  info "creating action \"$ACTION_NAME\" ..."
  ACTION_ID="$("$AUTH0_BIN" actions create -n "$ACTION_NAME" -t "$TRIGGER" -c "$(cat "$ACTION_CODE_FILE")" -r node22 --json | jq -r '.id')"
  [ -n "$ACTION_ID" ] || die "failed to create action"
  info "created action $ACTION_ID"
else
  info "action \"$ACTION_NAME\" exists ($ACTION_ID)"
fi

info "deploying action ..."
"$AUTH0_BIN" actions deploy "$ACTION_ID" >/dev/null

BIND_BODY="$(jq -nc --arg id "$ACTION_ID" '{bindings:[{ref:{type:"action_id",value:$id}}]}')"
printf '%s' "$BIND_BODY" | "$AUTH0_BIN" api patch "actions/triggers/$TRIGGER/bindings" >/dev/null
info "bound action to $TRIGGER trigger"
