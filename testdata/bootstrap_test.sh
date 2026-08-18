#!/usr/bin/env bash
#
# Regression test for bootstrap.sh's sdk-config.json emitter.
#
# The emitter replaces the Go implementation from PR #33; the golden file here
# is that PR's own testdata, so this asserts the shell port stayed faithful.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cp "$REPO_ROOT/testdata/env.livepeer.fixture" "$WORK/.env.livepeer"

# Isolate from the caller's shell — platform URL / billing overrides must not
# leak into the golden comparison.
env -u SIGNER_PROXY_URL -u SIGNER_PUBLIC_URL -u REMOTE_SIGNER_WEBHOOK_URL \
  -u OPENMETER_TRIAL_FEATURE_KEY \
  OPENMETER_URL=https://us.api.konghq.com/v3/openmeter/ \
  "$REPO_ROOT/bootstrap.sh" --skip-auth0 --skip-catalog --out "$WORK" >/dev/null 2>&1 \
  || fail "bootstrap.sh exited non-zero"

[ -f "$WORK/sdk-config.json" ] || fail "no sdk-config.json emitted"

diff <(jq -S . "$REPO_ROOT/testdata/sdk-config.golden.json") \
     <(jq -S . "$WORK/sdk-config.json") \
  || fail "sdk-config.json drifted from the PR #33 golden"
echo "ok: sdk-config.json matches golden"

# A trailing slash on OPENMETER_URL must not survive into the artifact.
grep -q '"url": "https://us.api.konghq.com/v3/openmeter"' "$WORK/sdk-config.json" \
  || fail "OPENMETER_URL trailing slash was not trimmed"
echo "ok: OPENMETER_URL normalised"

# An unknown app name must fail loudly rather than emit a half-filled config.
if env -u SIGNER_PROXY_URL -u SIGNER_PUBLIC_URL -u REMOTE_SIGNER_WEBHOOK_URL \
     OPENMETER_URL=x "$REPO_ROOT/bootstrap.sh" --skip-auth0 --skip-catalog \
     --out "$WORK" --app "No Such App" >/dev/null 2>&1; then
  fail "unknown app name should have failed"
fi
echo "ok: unknown app rejected"

# .env.livepeer is never sourced, so a command substitution in a value must not
# execute.
# shellcheck disable=SC2016  # the literal $(...) is the payload, not an expansion
printf 'DEMO_APP_AUTH0_PUBLIC_CLIENT_ID=$(touch %s/pwned)\n' "$WORK" >> "$WORK/.env.livepeer"
env -u SIGNER_PROXY_URL -u SIGNER_PUBLIC_URL -u REMOTE_SIGNER_WEBHOOK_URL \
  OPENMETER_URL=x "$REPO_ROOT/bootstrap.sh" --skip-auth0 --skip-catalog --out "$WORK" >/dev/null 2>&1 || true
[ ! -f "$WORK/pwned" ] || fail "a value in .env.livepeer was executed"
echo "ok: .env.livepeer is parsed, not sourced"

# Tenant-level Auth0 keys are written only when the provisioner could detect
# the active tenant. A blank one must stop the run, not emit a config that
# looks valid and fails at runtime.
for missing in AUTH0_DOMAIN AUTH0_ISSUER AUTH0_JWKS_URL; do
  BLANK="$(mktemp -d)"
  grep -v "^$missing=" "$REPO_ROOT/testdata/env.livepeer.fixture" > "$BLANK/.env.livepeer"
  if env -u SIGNER_PROXY_URL -u SIGNER_PUBLIC_URL -u REMOTE_SIGNER_WEBHOOK_URL \
       OPENMETER_URL=x "$REPO_ROOT/bootstrap.sh" --skip-auth0 --skip-catalog \
       --out "$BLANK" >/dev/null 2>&1; then
    rm -rf "$BLANK"; fail "missing $missing should have failed"
  fi
  [ ! -f "$BLANK/sdk-config.json" ] || { rm -rf "$BLANK"; fail "missing $missing still emitted sdk-config.json"; }
  rm -rf "$BLANK"
done
echo "ok: blank Auth0 tenant fields rejected"

# Billing ids from openmeter-billing.json are merged when present in --out.
printf '%s\n' '{
  "openmeter": {
    "sandboxAppId": "app_sandbox",
    "stripeAppId": "app_stripe",
    "freeBillingProfileId": "prof_free",
    "stripeBillingProfileId": "prof_stripe"
  }
}' > "$WORK/openmeter-billing.json"
env -u SIGNER_PROXY_URL -u SIGNER_PUBLIC_URL -u REMOTE_SIGNER_WEBHOOK_URL \
  OPENMETER_URL=https://us.api.konghq.com/v3/openmeter \
  "$REPO_ROOT/bootstrap.sh" --skip-auth0 --skip-catalog --out "$WORK" >/dev/null 2>&1 \
  || fail "bootstrap with billing json exited non-zero"
jq -e '
  .openmeter.sandboxAppId == "app_sandbox"
  and .openmeter.stripeAppId == "app_stripe"
  and .openmeter.freeBillingProfileId == "prof_free"
  and .openmeter.stripeBillingProfileId == "prof_stripe"
' "$WORK/sdk-config.json" >/dev/null \
  || fail "billing ids were not merged into sdk-config.json"
echo "ok: openmeter-billing.json merged into sdk-config"

echo "all bootstrap tests passed"
