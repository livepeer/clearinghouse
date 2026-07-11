#!/usr/bin/env bash
# One-time first publish for @pymthouse/clearinghouse-identity-webhook.
# npm OIDC cannot create a new package; use this once, then configure trusted publishing.
set -euo pipefail

cd "$(dirname "$0")/../identity-webhook"

npm ci
npm test

if [[ -z "${NPM_OTP:-}" ]]; then
  echo "Enter the 6-digit code from your authenticator app for npm 2FA:"
  read -r NPM_OTP
fi

npm publish --access public --ignore-scripts --otp="${NPM_OTP}"

echo ""
echo "Published. Next steps:"
echo "1. https://www.npmjs.com/package/@pymthouse/clearinghouse-identity-webhook/access"
echo "   → Trusted publishing → GitHub Actions"
echo "   → Repository: pymthouse/clearinghouse-runtime"
echo "   → Workflow: release.yml"
echo "2. Future releases: push v*.*.* tags (release.yml uses OIDC)"
