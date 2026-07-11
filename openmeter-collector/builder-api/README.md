# Clearinghouse Builder API

Go HTTP service co-located in the `openmeter-collector` container. Provisions **Auth0 end-users**, thin **OpenMeter session** (customer + subscription + optional trial grant), and mints **signer session JWTs** via Auth0.

Scalar docs: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

**Not a Konnect billing facade.** Catalog, plans, and usage queries belong to tenant Provisioner/Usage SPATs from [`konnect-credentials`](../../konnect-credentials/README.md). Builder-api only does session glue + pre-mint allowance gate.

## Token exchange flow

```text
resolve subject (JWT via identity-webhook, or sk_* API key)
  → ensure OpenMeter customer + default subscription (+ optional trial grant)
  → read credits/entitlement balance
  → if OPENMETER_ENFORCE_ALLOWANCE and !has_access: HTTP 402 insufficient_allowance
  → mint Auth0 signer JWT
  → response includes access_token + has_access + balance_usd_micros
```

Per-tenant OpenMeter credentials are loaded from
`GET {KONNECT_CREDENTIALS_URL}/v1/internal/tenants/{clientId}/openmeter`
(bound admin PAT). Falls back to `OPENMETER_URL` + `OPENMETER_API_KEY` for single-org local stacks.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/v1/apps/{clientId}/users` | M2M Basic | Create/upsert Auth0 user + OpenMeter customer; returns `apiKey` once |
| `POST` | `/api/v1/apps/{clientId}/oidc/token` | RFC 8693 form + subject token | Exchange Auth0 user JWT or `sk_*` API key for signer JWT; 402 if allowance exhausted |

## Auth0 prerequisites

Most credentials are written to `auth0-provisioner/provision/.env.livepeer` by `./bootstrap.sh` and mounted into the collector at `/service/.env.livepeer`.

### 1. Management API M2M application

`bootstrap.sh` creates **Clearinghouse Builder Management** (M2M) with Management API scopes `create:users`, `read:users`, `update:users`, and writes:

```bash
AUTH0_MGMT_CLIENT_ID=...
AUTH0_MGMT_CLIENT_SECRET=...
```

Re-run `./auth0-provisioner/provision/bootstrap.sh` if these are missing from `.env.livepeer`. Set `managementClient.enabled: false` in `apps.json` to skip.

Tenant domain, issuer, audience, and signer M2M credentials come from the same file. The entrypoint maps `DEMO_APP_AUTH0_M2M_*` → `AUTH0_SIGNER_M2M_*` automatically.

### 2. Database connection

Enable a Database connection (default: `Username-Password-Authentication`) for end-user records. Set `AUTH0_DB_CONNECTION` if you use a different connection name.

### 3. Credentials-exchange Action (`external_user_id` + `client_id` claims)

Signer-token mint uses M2M `client_credentials` with `scope=sign:mint_user_token`, form fields `external_user_id`, and `client_id` (the **public** app client id from the Builder API path). Auth0 does not pass custom form fields into access tokens unless an **Action** adds them.

**Deploy manually** in the Auth0 dashboard (Actions → Library → Build Custom → Credentials Exchange trigger):

```javascript
exports.onExecuteCredentialsExchange = async (event, api) => {
  const externalUserId = event.request?.body?.external_user_id;
  const clientId = event.request?.body?.client_id;
  if (!externalUserId || !clientId) {
    return;
  }
  api.accessToken.setCustomClaim("external_user_id", externalUserId);
  api.accessToken.setCustomClaim("app_client_id", clientId);
};
```

1. Deploy the Action.
2. Bind it to the **Credentials Exchange** flow for your tenant.
3. Re-test token exchange — the minted JWT must include `external_user_id` and `app_client_id` for [identity-webhook](../../identity-webhook) OIDC verification (`OIDC_SUBJECT_CLAIM=external_user_id`, `OIDC_CLIENT_CLAIM=app_client_id`). Auth0 rejects the reserved claim name `client_id`; use `app_client_id` instead.

Without this Action, minted tokens verify at Auth0 but lack identity claims and the webhook rejects them.

### 5. Identity-webhook (JWT subject-token verification)

JWT `subject_token`s are **not** verified in-process. The Builder API forwards them to the
[identity-webhook](../../identity-webhook) `POST /authorize` contract, which owns Auth0 JWKS
verification and claim extraction. `sk_*` API-key subject tokens are still resolved directly
against Auth0 `app_metadata` by the Builder API.

Set both to enable JWT exchange (defaults `IDENTITY_WEBHOOK_URL` to `REMOTE_SIGNER_WEBHOOK_URL`):

```bash
IDENTITY_WEBHOOK_URL=http://identity-webhook:8090
WEBHOOK_SECRET=...   # shared with the identity-webhook
```

The webhook must run in `IDENTITY_AUTH_MODE=oidc` to verify JWTs. If unset, JWT subject tokens
are rejected with `invalid_grant` (API-key exchange still works).

### 4. Signer M2M (from bootstrap)

Provided automatically via mounted `.env.livepeer` (`DEMO_APP_AUTH0_M2M_CLIENT_ID` /
`DEMO_APP_AUTH0_M2M_CLIENT_SECRET`). Override in `openmeter-collector/.env` only if needed.

## Example: create user

```bash
set -a; source openmeter-collector/.env; set +a
CLIENT_ID="$AUTH0_SIGNER_M2M_CLIENT_ID"   # or your public client id path param
curl -sS -u "$AUTH0_SIGNER_M2M_CLIENT_ID:$AUTH0_SIGNER_M2M_CLIENT_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"externalUserId":"user-123","email":"user@example.com"}' \
  "http://localhost:8095/api/v1/apps/${DEMO_APP_AUTH0_PUBLIC_CLIENT_ID}/users"
```

Use the public client id from `.env.livepeer` as the `{clientId}` path segment (e.g. `DEMO_APP_AUTH0_PUBLIC_CLIENT_ID`).

## Example: RFC 8693 signer session exchange

Signer-session issuance uses **RFC 8693** at `POST /api/v1/apps/{clientId}/oidc/token` with `application/x-www-form-urlencoded` body fields:

- `{clientId}` — public Auth0 client id for the integrator app (same path segment as `/users`)
- `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`
- `subject_token` — Auth0 user access token (device code) **or** end-user API key (`sk_*`)
- `subject_token_type=urn:ietf:params:oauth:token-type:access_token`
- `audience=livepeer-clearinghouse` (or omit; must match configured audience when provided)

The subject token must belong to the public client named in the path (`azp` for JWTs, or API key issued for that client). Optional HTTP Basic auth with the signer M2M client is supported for server-side callers.

API key:

```bash
set -a; source openmeter-collector/.env; set +a
PUBLIC_CLIENT_ID="$DEMO_APP_AUTH0_PUBLIC_CLIENT_ID"
API_KEY=sk_...
curl -sS \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  --data-urlencode "subject_token=$API_KEY" \
  --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "audience=livepeer-clearinghouse" \
  "http://localhost:8095/api/v1/apps/${PUBLIC_CLIENT_ID}/oidc/token"
```

Device code (user JWT as `subject_token`):

```bash
PUBLIC_CLIENT_ID="$DEMO_APP_AUTH0_PUBLIC_CLIENT_ID"
OIDC_TOKEN=...   # access_token from device code flow
curl -sS \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  --data-urlencode "subject_token=$OIDC_TOKEN" \
  --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "audience=livepeer-clearinghouse" \
  "http://localhost:8095/api/v1/apps/${PUBLIC_CLIENT_ID}/oidc/token"
```

The OpenMeter customer key is `{clientId}:{sub}` (e.g. `pub:google-oauth2|…`), matching the CloudEvent `subject`.

Successful exchange JSON includes:

```json
{
  "access_token": "...",
  "token_type": "Bearer",
  "expires_in": 300,
  "scope": "sign:job",
  "has_access": true,
  "balance_usd_micros": 1000000
}
```

When allowance is exhausted (`OPENMETER_ENFORCE_ALLOWANCE=true`, default):

```json
HTTP 402
{ "error": "insufficient_allowance", "error_description": "trial credits exhausted; ..." }
```

## OpenMeter customer key

Customers are upserted with:

- `key`: `{clientId}:{externalUserId}`
- `usage_attribution.subject_keys`: `["{clientId}:{externalUserId}"]`

This matches the collector CloudEvent `subject` / `auth_id` contract.

Plans, meters, and usage queries: use tenant SPATs from
[`konnect-credentials`](../../konnect-credentials/README.md) against Kong directly.

## Env (metering)

| Variable | Purpose |
| --- | --- |
| `KONNECT_CREDENTIALS_URL` | Per-tenant OpenMeter cred lookup |
| `PLATFORM_API_SECRET` | Auth to credentials service |
| `OPENMETER_URL` / `OPENMETER_API_KEY` | Single-org fallback |
| `OPENMETER_DEFAULT_PLAN_KEY` | Default `clearinghouse_default_ppu` |
| `OPENMETER_TRIAL_FEATURE_KEY` | Default `billable_spend` |
| `OPENMETER_TRIAL_GRANT_USD_MICROS` | `0` disables auto trial grant |
| `OPENMETER_ENFORCE_ALLOWANCE` | Default `true` |

## Local development

```bash
cd openmeter-collector/builder-api
go run ./cmd/builder-api
```

Requires the same env vars as the container (`openmeter-collector/.env`).
