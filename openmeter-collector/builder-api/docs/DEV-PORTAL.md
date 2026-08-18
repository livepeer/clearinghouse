# Dev Portal + Auth0 DCR + Kong OIDC edge

Operator runbook for publishing the builder-api **usage** surface on Kong
Konnect Dev Portal with an **Auth0** application auth strategy, and putting
usage traffic behind a Gateway Service so Kong’s OpenID Connect plugin
validates Auth0 tokens at the edge.

Audience API identifier: **`livepeer-clearinghouse`**.

## Hosts (do not mix these up)

| Host | What it is | Use for |
| --- | --- | --- |
| `https://livepeer.pymthouse.com` | Kong Dev Portal custom domain | Docs, DCR “create app”, playground UI origin (CORS) |
| `https://builder-api-production-82bf.up.railway.app` | Railway **builder-api** | Usage, user upsert, RFC 8693 signer session |
| `https://pymthouse.us.auth0.com` | Auth0 tenant | `POST /oauth/token` signer mint (`sign:mint_user_token`) |
| `$KONNECT_PROXY_URL` | Kong Gateway (optional Phase C) | Edge OIDC for JWT / DCR Bearer only |

`livepeer.pymthouse.com` is **not** an API. Minting or `POST /api/v1/oidc/token` against that host returns portal HTML / CloudFront 403.

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

Until a Gateway Service is linked, OpenAPI `servers` / playground default to
Railway builder-api (`https://builder-api-production-82bf.up.railway.app`).
After Phase C, put `$KONNECT_PROXY_URL` first for JWT / DCR Bearer; keep Railway
documented for `sk_*`.

Playground browser origins that need CORS on builder-api:

- `https://*.kongportals.com` (`CORS_ALLOW_KONG_PORTALS=true` by default)
- `https://livepeer.pymthouse.com` (`CORS_ALLOWED_ORIGINS`)

### Signer mint (Auth0) vs SignerSession (builder-api)

```bash
# 1) Short-lived signer JWT — Auth0 (not the portal host)
curl -sS -X POST 'https://pymthouse.us.auth0.com/oauth/token' \
  -u "$CLIENT_ID:$CLIENT_SECRET" \
  -H 'content-type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'audience=livepeer-clearinghouse' \
  --data-urlencode 'scope=sign:mint_user_token' \
  --data-urlencode "external_user_id=$EXTERNAL_USER_ID" \
  --data-urlencode "client_id=$CLIENT_ID"

# 2) Full SignerSession envelope — Railway builder-api
export BUILDER_API=https://builder-api-production-82bf.up.railway.app
# POST $BUILDER_API/api/v1/apps/$CLIENT_ID/oidc/token  (RFC 8693)
```

---

## Phase B — Portal + Auth0 strategy

### Form mapping ([create auth strategy](https://cloud.konghq.com/us/portals/application-auth/auth-strategy/create))

The UI “New DCR Provider” panel is the Auth0 DCR provider **plus** the
application auth strategy. Map fields as follows:

| UI field | Value |
| --- | --- |
| Name | `Auth0 Production` (or `AUTH0_DCR_PROVIDER_NAME`) |
| Provider Type | **Auth0** — immutable after create |
| Issuer URL | Auth0 tenant root, e.g. `https://pymthouse.us.auth0.com` |
| Client ID / Client Secret | Auth0 M2M **“Konnect Portal DCR Admin”** (Management API), **not** the clearinghouse signer M2M and **not** what callers use for usage |
| Client Audience | Usually empty; set only for Auth0 custom domains (`…/api/v2/`) |
| Use Developer Managed Scopes | off (unless you need portal-managed scopes) |
| Scopes | `sign:mint_user_token,sign:job` (must exist on Auth0 API `livepeer-clearinghouse`; override via `AUTH0_DCR_SCOPES`) |
| Credential Claims | `azp` |
| Auth Methods | `client_credentials`, `bearer` |
| SSL verify | on |

Audience for tokens issued to portal apps is configured on the **strategy** as
`livepeer-clearinghouse` (`token_post_args` / `AUTH0_DCR_AUDIENCE`), not as the
DCR admin Client Audience.

### JWT-scoped usage (what this enables)

Yes — with the strategy linked to a Gateway Service, Konnect validates Auth0
Bearer / client_credentials at the edge, then builder-api scopes rows to the
authenticated **actor** + path `clientId`.

| Token | Who | Usage result |
| --- | --- | --- |
| End-user Auth0 JWT (`bearer`) | Real user / app user | Actor-scoped rows (intended self-serve path) |
| Portal DCR app `client_credentials` | App-level `azp` | Passes Gateway if OIDC accepts it; builder-api still needs a user JWT or `sk_*` identity for actor isolation |
| DCR Admin M2M | Konnect → Auth0 Management only | **Never** call usage with this |

### Bootstrap (Auth0 then Konnect)

Demo default: **one M2M** (`DEMO_APP_AUTH0_M2M_*`) is both signer mint and Konnect
DCR admin (two Auth0 client grants: `livepeer-clearinghouse` + Management API).
Production can set `AUTH0_DCR_DEDICATED=1` for a separate least-privilege app.

```bash
# 1) Ensure Demo M2M has Management API DCR scopes
./openmeter-collector/provision/bootstrap.sh auth0-dcr

# 2) Konnect DCR provider + strategy
./openmeter-collector/provision/bootstrap.sh portal-dcr

# 3) Portal + usage API + publish with auth_strategy_ids
./openmeter-collector/provision/bootstrap.sh portal-publish
```

`portal-publish` creates/finds portal `clearinghouse` and API `clearinghouse-usage`,
uploads builder-api OpenAPI, and `PUT`s the publication with the Auth0 DCR strategy.
Optional: `KONNECT_CONTROL_PLANE_ID` + `KONNECT_SERVICE_ID` to link a Gateway Service.

### 1. Portal and API

1. Create a Dev Portal and a page for clearinghouse usage.
2. Create an API from builder-api OpenAPI (`GET /api/v1/openapi.json`), scoped
   to `GET /api/v1/apps/{clientId}/usage` (and optional `/health` only).

### 2. Auth0 M2M for Konnect DCR admin

Create an Auth0 Machine-to-Machine application (“Konnect Portal DCR Admin”)
with Management API scopes from the
[Auth0 DCR how-to](https://developer.konghq.com/how-to/auth0-dcr/).

### 3–5. Provider, strategy, publish

Prefer `bootstrap.sh portal-dcr` (above). Equivalent API steps:

1. DCR provider: `provider_type: auth0`, issuer, `initial_client_id` / `initial_client_secret`
2. Application auth strategy: `openid_connect`, `credential_claim: ["azp"]`,
   `auth_methods: ["client_credentials","bearer"]`, audience `livepeer-clearinghouse`,
   scopes `sign:mint_user_token` + `sign:job` (`AUTH0_DCR_SCOPES`)
3. `PUT …/publications/{portalId}` with `auth_strategy_ids` (or set
   `KONNECT_PORTAL_ID` + `KONNECT_API_ID` so bootstrap publishes)

---

## Phase C — Gateway Service (OIDC edge)

1. **One service** with upstream = Railway builder-api base URL.
2. **Routes** (usage only):
   - `/api/v1/apps/{clientId}/usage` (and path params as Konnect requires)
   - Optional: `/health` only
   - **Do not** expose `/authorize`, `POST …/oidc/token`, or signer bus routes
3. Link the published API / apply the Auth0 OIDC plugin from the portal strategy.
4. Move OpenAPI `servers` / playground to `$KONNECT_PROXY_URL` (Railway stays
   as a secondary server for `sk_*`).
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

- [x] Script DCR provider + auth strategy via `bootstrap.sh portal-dcr`
- [x] Portal publish with auth strategy via `bootstrap.sh portal-publish`
- [ ] Create Auth0 M2M + run `portal-dcr` in the target org
- [ ] Publish usage OpenAPI with `auth_strategy_ids` and gateway link
- [ ] Apply TLS verify depth on self-managed data planes
- [x] OpenAPI `servers` default to Railway builder-api (pre-gateway)
- [ ] After Gateway link: put live `$KONNECT_PROXY_URL` first in OpenAPI servers
- [ ] Sync `provision/gateway/kong.yaml` via decK after first successful apply
