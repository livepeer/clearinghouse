# Querying usage (user-scoped)

How end users query metered usage from builder-api using signer JWT Bearer
authentication.

Interactive OpenAPI: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

---

## Endpoint

### `GET /api/v1/users/me/usage`

Identity is derived from JWT claims (`app_client_id`, `external_user_id`) via
identity-webhook verification. The server always queries exactly one usage
subject: `{clientId}:{externalUserId}`.

| Query | Required | Notes |
| --- | --- | --- |
| `meter` | yes | Catalog meter key |
| `from` / `to` | no | RFC3339 window |
| `groupBy` | no | Additional group-by dimensions |

```bash
export BUILDER_API=https://builder-api-production-82bf.up.railway.app
export SIGNER_JWT=eyJ...

curl -sS \
  -H "Authorization: Bearer $SIGNER_JWT" \
  "$BUILDER_API/api/v1/users/me/usage?meter=billable_usd_micros" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "externalUserId": "demo-user",
  "meter": "billable_usd_micros",
  "subject": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:demo-user",
  "rows": []
}
```

---

## Token minting

Mint signer JWTs through RFC 8693 token exchange:

### `POST /api/v1/oidc/token`

Required form fields:

- `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`
- `subject_token`
- `subject_token_type=urn:ietf:params:oauth:token-type:access_token`

Optional:

- `requested_token_type=urn:ietf:params:oauth:token-type:access_token`
- `audience=livepeer-clearinghouse`
- `resource=livepeer-clearinghouse`

```bash
curl -sS \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  --data-urlencode "subject_token=$SUBJECT_TOKEN" \
  --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "audience=livepeer-clearinghouse" \
  "$BUILDER_API/api/v1/oidc/token"
```

`subject_token` may be an end-user JWT or an API key (`sk_*`).

---

## Status codes

| Code | Meaning |
| --- | --- |
| `400` | Invalid query params (`meter`, window) |
| `401` | Missing/invalid Bearer token (usage) or invalid client/token (exchange) |
| `402` | `insufficient_allowance` on token exchange when allowance enforcement is enabled |
| `502` | OpenMeter query failure |
| `503` | JWT verifier or metering backend not configured |
