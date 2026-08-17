# Dev Portal + Auth0 DCR + Kong OIDC edge

Operator runbook for publishing the builder-api **usage** surface on Kong
Konnect Dev Portal with an **Auth0** application auth strategy, and putting
usage traffic behind a Gateway Service so Kong’s OpenID Connect plugin
validates Auth0 tokens at the edge.

Audience API identifier: **`livepeer-clearinghouse`**.

Companion docs: [USAGE.md](USAGE.md), [TENANT-ISOLATION.md](TENANT-ISOLATION.md).
Upstream how-tos: [Auth0 DCR](https://developer.konghq.com/how-to/auth0-dcr/),
[Dev Portal DCR auth methods](https://developer.konghq.com/dev-portal/dynamic-client-registration/#authentication-methods),
[OIDC for Dev Portal](https://developer.konghq.com/how-to/enable-oidc-auth-for-dev-portal/#prerequisites).

---

## Roles required (Konnect)

| Role | Why |
| --- | --- |
| Portal Creator | Create the Dev Portal |
| Content Editor | Portal pages / branding |
| Gateway Service Admin | Service, routes, plugins |
| API Creator | Register the usage OpenAPI as an API |
| Auth Strategy Creator (DCR Provider Creator) | Auth0 DCR provider + application auth strategy |
| API Publisher | `PUT …/publications/{portalId}` with `auth_strategy_ids` |

---

## Architecture (what each layer does)

| Layer | Product | Role |
| --- | --- | --- |
| Metrics | OpenMeter (Metering & Billing) | Usage rows |
| Edge auth | Kong Gateway + Auth0 strategy | Validate Auth0 Bearer / client_credentials |
| Docs / DCR UX | Dev Portal | Catalog, register apps, mint Auth0 clients |
| Business rules | builder-api | Actor + `clientId` isolation, catalog meters |

Dev Portal alone does **not** proxy. Publishing with an Auth0 strategy **and**
linking the API to a Gateway Service is what makes Kong enforce OIDC on the
data plane.

---

## Credential split

| Credential | Gateway (`$KONNECT_PROXY_URL`) | Railway builder-api |
| --- | --- | --- |
| Auth0 user JWT / DCR Bearer | Yes | Yes |
| `sk_*` | **No** (do not add Key Auth in v1) | Yes |

Playground / OpenAPI `servers` for the published API should point at
`$KONNECT_PROXY_URL`. Document Railway for `sk_*` or after exchanging for a JWT
via existing token flows.

---

## Phase B — Portal + Auth0 strategy

### 1. Portal and API

1. Create a Dev Portal and a page for clearinghouse usage.
2. Create an API from builder-api OpenAPI (`GET /api/v1/openapi.json`), scoped
   to `GET /api/v1/apps/{clientId}/usage` (and optional `/health` only).

### 2. Auth0 M2M for Konnect DCR admin

Create an Auth0 Machine-to-Machine application (“Konnect Portal DCR Admin”)
with Management API scopes from the
[Auth0 DCR how-to](https://developer.konghq.com/how-to/auth0-dcr/).

### 3. DCR provider (`provider_type: auth0`)

Create the DCR provider with:

- `provider_type`: **`auth0`** — **immutable after create**; choose Auth0 once
- Issuer URL for the Auth0 tenant
- `initial_client_id` / `initial_client_secret` from the M2M app above

### 4. Application auth strategy

Configure an `openid_connect` application auth strategy:

- `credential_claim`: `["azp"]`
- `auth_methods`: `["client_credentials", "bearer"]`
- Audience: **`livepeer-clearinghouse`**

### 5. Publish

`PUT …/publications/{portalId}` including `auth_strategy_ids` for the Auth0
strategy. Developers register apps in the portal; Konnect DCR creates Auth0
clients when applicable.

---

## Phase C — Gateway Service (OIDC edge)

1. **One service** with upstream = Railway builder-api base URL.
2. **Routes** (usage only):
   - `/api/v1/apps/{clientId}/usage` (and path params as Konnect requires)
   - Optional: `/health` only
   - **Do not** expose `/authorize`, `POST …/oidc/token`, or signer bus routes
3. Link the published API / apply the Auth0 OIDC plugin from the portal strategy.
4. Set OpenAPI `servers` + playground base URL to `$KONNECT_PROXY_URL`.
5. Optional checked-in declarative config:
   [`provision/gateway/kong.yaml`](../../provision/gateway/kong.yaml) via
   [decK](https://developer.konghq.com/deck/get-started/).

### Self-managed data plane TLS checklist

Auth0 uses a Let’s Encrypt chain. Kong 3.14+ default verify depth **1** is too
shallow ([Auth0 DCR note](https://developer.konghq.com/how-to/auth0-dcr/#apply-the-auth0-dcr-auth-strategy-to-an-api)):

- `lua_ssl_verify_depth` ≥ `2` in `kong.conf`
- `lua_ssl_trusted_certificate` includes `system`
- Restart the data plane after changes

Skip or verify cloud equivalents if using Dedicated Cloud Gateways.

### OIDC debug (if Phase C fails)

From the [OIDC plugin debugging guide](https://developer.konghq.com/plugins/openid-connect/#debugging-the-oidc-plugin):

- Log level `debug`, filter `openid-connect`
- Temporarily `config.display_errors`
- Isolate with temporary disable of `verify_nonce` / `verify_claims` /
  `verify_signature` (restore immediately after diagnosis)
- Large session cookies → Redis/memcache session storage if needed

Unauthenticated requests must be rejected at the Gateway. Builder-api still
enforces actor/`clientId` even after a valid edge token.

---

## Success checks

1. Portal DCR + Auth0 strategy works against the gateway-linked publication.
2. Bearer via `$KONNECT_PROXY_URL` returns actor-scoped OpenMeter rows.
3. Unauthenticated calls fail at Gateway.
4. `sk_*` works on Railway; unsupported through Gateway (documented).
5. Self-managed DP TLS depth applied where applicable.

---

## Implementation tasks

- [ ] Create Auth0 M2M + DCR provider (`provider_type: auth0`) in the target org.
- [ ] Publish usage OpenAPI with `auth_strategy_ids` and gateway link.
- [ ] Apply TLS verify depth on self-managed data planes.
- [ ] Replace OpenAPI `${KONNECT_PROXY_URL}` with the live proxy hostname.
- [ ] Sync `provision/gateway/kong.yaml` via decK after first successful apply.
