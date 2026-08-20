# Querying usage (user-scoped)

How end users query metered usage from builder-api using signer JWT Bearer
authentication.

Interactive OpenAPI: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

---

## Endpoint

### `GET /api/v1/users/me/usage`

Identity is derived from JWT claims (`app_client_id`, `external_user_id`) via
identity-webhook verification. The server always queries exactly one usage
subject: `{clientId}:{externalUserId}` and one meter slug from deployment
config (`OPENMETER_TRIAL_FEATURE_KEY` via the provisioned catalog).

| Query | Required | Notes |
| --- | --- | --- |
| `from` / `to` | no | RFC3339 window |
| `groupBy` | no | Additional group-by dimensions |

```bash
export BUILDER_API=https://builder-api-production-82bf.up.railway.app
export SIGNER_JWT=eyJ...

curl -sS \
  -H "Authorization: Bearer $SIGNER_JWT" \
  "$BUILDER_API/api/v1/users/me/usage" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "externalUserId": "demo-user",
  "meter": "network_fee_usd_micros",
  "subject": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:demo-user",
  "rows": []
}
```

### `GET /api/v1/users/me/balance`

Same identity rules. Returns the live OpenMeter credit/entitlement snapshot
for `OPENMETER_TRIAL_FEATURE_KEY` (the same source token exchange uses for
allowance). No query parameters.

```bash
curl -sS \
  -H "Authorization: Bearer $SIGNER_JWT" \
  "$BUILDER_API/api/v1/users/me/balance" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "externalUserId": "demo-user",
  "subject": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:demo-user",
  "feature": "network_spend",
  "hasAccess": true,
  "balanceUsdMicros": 5000000,
  "source": "credits"
}
```

### `GET /api/v1/users/me/payment-method`

Same identity rules. Returns whether Konnect has a default Stripe payment
method on the OpenMeter customer. Missing Stripe app or billing data is
`hasDefaultPaymentMethod: false`. Konnect/OpenMeter transport or HTTP errors
are `502`, not a false negative.

```bash
curl -sS \
  -H "Authorization: Bearer $SIGNER_JWT" \
  "$BUILDER_API/api/v1/users/me/payment-method" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "externalUserId": "demo-user",
  "subject": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:demo-user",
  "hasDefaultPaymentMethod": false,
  "stripeCustomerId": ""
}
```

### `POST /api/v1/users/me/payment-method`

Starts OpenMeter/Konnect Stripe Checkout in setup mode for the same customer.
`successUrl` and `cancelUrl` must be `https` with a host and no userinfo.
The response `checkoutUrl` is always a `checkout.stripe.com` host. The shared
OpenMeter org must have the Stripe app installed.

```bash
curl -sS \
  -H "Authorization: Bearer $SIGNER_JWT" \
  -H "Content-Type: application/json" \
  -d '{"successUrl":"https://app.example.com/billing/ok","cancelUrl":"https://app.example.com/billing/cancel"}' \
  "$BUILDER_API/api/v1/users/me/payment-method" | jq .
```

Example shape:

```json
{
  "checkoutUrl": "https://checkout.stripe.com/c/pay/cs_test_xxx",
  "sessionId": "cs_test_xxx"
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
| `400` | Invalid query params (time window) or non-https checkout redirect URLs |
| `401` | Missing/invalid Bearer token (usage) or invalid client/token (exchange) |
| `402` | `insufficient_allowance` on token exchange when allowance enforcement is enabled |
| `404` | OpenMeter customer not found (balance / payment method) |
| `502` | OpenMeter query or checkout session failure |
| `503` | JWT verifier or metering backend not configured |
