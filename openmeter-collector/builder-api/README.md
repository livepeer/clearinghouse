# Clearinghouse Builder API

> **Landing status.** This PR ships the service only. The companion
> `auth0-provisioner/` (Auth0 tenant + Credentials Exchange Action) and the
> collector wiring that runs this binary land in follow-up PRs, so the paths
> referenced under [Auth0 prerequisites](#auth0-prerequisites) do not exist in
> the tree yet.

Go HTTP service co-located in the `openmeter-collector` container. Provisions **Auth0 end-users**, thin **OpenMeter session** (customer + subscription + optional trial grant), and mints **signer session JWTs** via Auth0.

Scalar docs: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

**Usage surface.** End users read their own usage with signer JWT Bearer auth against a single shared OpenMeter organization. See [docs/USAGE.md](docs/USAGE.md) for curl examples.

## Usage route

User-scoped usage route:

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/users/me/usage?from=&to=[&groupBy=]` | Bearer signer JWT | Metered usage for the authenticated user only |

The server derives `clientId` + `externalUserId` from token claims and queries exactly one usage subject.

### User self route

End users can read only their own usage with a signer JWT, without passing an
app id in the path:

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/users/me/usage?from=&to=[&groupBy=]` | Bearer signer JWT | Metered usage for the authenticated user only |

Identity is derived from JWT claims (`app_client_id`, `external_user_id`) via
the identity-webhook verifier.

## Token exchange flow

```text
resolve subject (JWT via identity-webhook, or sk_* API key)
  → ensure OpenMeter customer + default subscription (+ optional trial grant)
  → read credits/entitlement balance
  → if OPENMETER_ENFORCE_ALLOWANCE and !has_access: HTTP 402 insufficient_allowance
  → mint Auth0 signer JWT
  → response includes access_token + has_access + balance_usd_micros
```

## Shared OpenMeter organization

`OPENMETER_URL` + `OPENMETER_API_KEY` are the credentials for **one** Kong
OpenMeter organization shared by all tenants. Isolation is by customer key
(`{clientId}:{externalUserId}`); the tenant boundary is enforced in the admin
API, not by issuing each tenant its own Konnect credentials.

There is no per-tenant lookup against `konnect-credentials`. That service is
parked. Builder-api talks only to the shared org.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/v1/apps/{clientId}/users` | M2M Basic | Create/upsert Auth0 user + OpenMeter customer; returns `apiKey` once |
| `POST` | `/api/v1/users/me/api-key` | Subject token (Bearer or form) | Rotate end-user API key; client inferred from token |
| `POST` | `/api/v1/oidc/token` | RFC 8693 form + subject token | Exchange Auth0 user JWT or `sk_*` API key for signer JWT; client inferred from token |

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

### 4. Signer M2M (from bootstrap)

Provided automatically via mounted `.env.livepeer` (`DEMO_APP_AUTH0_M2M_CLIENT_ID` /
`DEMO_APP_AUTH0_M2M_CLIENT_SECRET`). Override in `openmeter-collector/.env` only if needed.

### 5. Identity-webhook (JWT subject-token verification)

JWT `subject_token`s are **not** verified in-process. The Builder API forwards them to the
[identity-webhook](../../identity-webhook) `POST /authorize` contract, which owns Auth0 JWKS
verification and claim extraction. `sk_*` API-key subject tokens are still resolved directly
against Auth0 `app_metadata` by the Builder API.

Set both to enable JWT exchange (same vars as the remote signer):

```bash
REMOTE_SIGNER_WEBHOOK_URL=http://identity-webhook:8090/authorize
WEBHOOK_SECRET=...   # shared with the identity-webhook
```

The webhook must run in `IDENTITY_AUTH_MODE=oidc` to verify JWTs. If unset, JWT subject tokens
are rejected with `invalid_grant` (API-key exchange still works).

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

## Example: rotate end-user API key

When an end-user loses their `sk_*` key (or it is compromised), rotate using the
same subject tokens accepted by token exchange. Identity is inferred from the
token — no `clientId` in the path.

Bearer (existing API key or Auth0 user JWT):

```bash
curl -sS -X POST \
  -H "Authorization: Bearer $SUBJECT_TOKEN" \
  "http://localhost:8095/api/v1/users/me/api-key"
```

Form body (same shape as token exchange subject fields):

```bash
curl -sS -X POST \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "subject_token=$SUBJECT_TOKEN" \
  "http://localhost:8095/api/v1/users/me/api-key"
```

JSON body:

```bash
curl -sS -X POST \
  -H "Content-Type: application/json" \
  -d "{\"subject_token\":\"$SUBJECT_TOKEN\"}" \
  "http://localhost:8095/api/v1/users/me/api-key"
```

Response:

```json
{
  "clientId": "...",
  "externalUserId": "user-123",
  "apiKey": "sk_...",
  "status": "active"
}
```

## Example: RFC 8693 signer session exchange

Signer-session issuance uses **RFC 8693** at `POST /api/v1/oidc/token`. JSON is
preferred; `application/x-www-form-urlencoded` is also accepted (OAuth 2.0 /
RFC 8693 default).

JSON (preferred):

```bash
set -a; source openmeter-collector/.env; set +a
API_KEY=sk_...
curl -sS \
  -H "Content-Type: application/json" \
  -d "$(jq -n \
    --arg subject "$API_KEY" \
    '{
      grant_type: "urn:ietf:params:oauth:grant-type:token-exchange",
      subject_token: $subject,
      subject_token_type: "urn:ietf:params:oauth:token-type:access_token",
      requested_token_type: "urn:ietf:params:oauth:token-type:access_token",
      audience: "livepeer-clearinghouse"
    }')" \
  "http://localhost:8095/api/v1/oidc/token"
```

Form body (OAuth-compatible):

```bash
curl -sS \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  --data-urlencode "subject_token=$API_KEY" \
  --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "audience=livepeer-clearinghouse" \
  "http://localhost:8095/api/v1/oidc/token"
```

The subject token determines the target app (`azp`/`app_client_id` for JWTs, or
API key ownership for `sk_*`). Optional HTTP Basic auth with the signer M2M
client is supported for server-side callers.

Device code (user JWT as `subject_token`):

```bash
OIDC_TOKEN=...   # access_token from device code flow
curl -sS \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  --data-urlencode "subject_token=$OIDC_TOKEN" \
  --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  --data-urlencode "audience=livepeer-clearinghouse" \
  "http://localhost:8095/api/v1/oidc/token"
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

Plans and rate cards are still provisioned out of band by
`openmeter-collector/provision/bootstrap.sh`. Usage reads go through
`GET /api/v1/users/me/usage`.

## Env (metering)

| Variable | Purpose |
| --- | --- |
| `OPENMETER_URL` / `OPENMETER_API_KEY` | Shared Kong OpenMeter organization (required) |
| `OPENMETER_DEFAULT_PLAN_KEY` | Default `clearinghouse_default_ppu` |
| `OPENMETER_TRIAL_FEATURE_KEY` | Default `network_spend` (maps to usage meter via catalog) |
| `OPENMETER_USAGE_METER_KEY` | Optional override for usage queries (default derived from trial feature) |
| `OPENMETER_TRIAL_GRANT_USD_MICROS` | `0` disables auto trial grant |
| `OPENMETER_ENFORCE_ALLOWANCE` | Default `true` |

## Local development

```bash
cd openmeter-collector/builder-api
go run ./cmd/builder-api
```

Requires the same env vars as the container (`openmeter-collector/.env`).
