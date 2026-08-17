# Tenant isolation on a shared OpenMeter instance

> **Decided.** `livepeer/clearinghouse` uses **one** Kong OpenMeter
> organization as a shared tenant. This is settled architecture, not an open
> option — the per-tenant-organization alternative described below is recorded
> because it was seriously considered and because the constraint behind it
> still shapes this design, not because it remains on the table.

How the clearinghouse serves many tenants from **one** OpenMeter/Konnect
organization, what enforces the boundary between them, and the manual steps an
operator must complete before the admin API can run.

Closes the isolation and prerequisite-documentation parts of
[#12](https://github.com/livepeer/clearinghouse/issues/12).

---

## The model

One Konnect organization. One platform credential, held only by the
clearinghouse. Tenants never receive Konnect credentials and never call the
Metering & Billing API directly — every read goes through the builder-api admin
routes, which are the only place a tenant boundary can be enforced.

```
tenant A ─┐
tenant B ─┼─► builder-api admin routes ──► one Konnect org
tenant C ─┘   (per-tenant authorization)    (one platform SPAT)
```

Usage is partitioned by **customer key**, which is
`CustomerKey(clientID, externalUserID)` → `"{clientId}:{externalUserId}"`. This
is the same key the collector emits as the CloudEvent subject and the same
compound identity the identity-webhook issues, so the partition is consistent
end to end rather than being a convention only the admin API believes in.

### Why not one Konnect organization per tenant

The alternative — bind each tenant to their own Konnect org and hand them
scoped SPATs — is a real design, and it is the one `konnect-credentials/`
implements. It is not what this repo ships, for two reasons:

1. **Konnect has no public Create-Organization API.** Every tenant org has to
   be created by hand in the console. That is incompatible with the
   one-command bootstrap in [#9](https://github.com/livepeer/clearinghouse/issues/9)
   and puts a human in the path of every onboarding.
2. **It moves the boundary out of our reach.** If tenants hold Konnect
   credentials and query Kong directly, the clearinghouse cannot enforce,
   audit, or revoke anything about those queries.

The premise behind per-tenant orgs is correct and worth restating, because it
is exactly what this design has to work around: **Konnect metering roles are
org-wide.** A SPAT in a shared org can read every customer in that org. That is
precisely why no tenant ever gets one here — the platform SPAT stays inside the
clearinghouse, and the boundary is enforced above it in the admin API.

BYO-org would be a substantial re-architecture, not a configuration flag: the
resolver that looked credentials up per tenant has been removed, and the admin
API now assumes one organization throughout. Revisiting it means revisiting
this document.

---

## What enforces the boundary

### Usage route (`GET …/usage`)

Self-serve reads. Layers in request order:

| # | Layer | Where | What it stops |
|---|---|---|---|
| 0 | Kong OIDC (optional edge) | Gateway + Auth0 strategy | Unauthenticated / non-Auth0 Bearer before Railway |
| 1 | Bearer authentication | `authorizeUsageActor` | Missing Bearer, bad JWT, bad `sk_*`, or M2M Basic → 401 |
| 2 | Path / app match | `authorizeUsageActor` | Credential app ≠ path `clientId` → 404 |
| 3 | Catalog + actor filter query | `handleUsage` | Non-catalog meters; `externalUserId` ≠ actor → 400 |
| 4 | Dimension scope | `QueryUsage` | Empty `client_id` never hits Konnect; wire filter is `filters.dimensions.client_id` |
| 5 | Response filtering | `filterRowsToActor` | Backend rows for other users under the same app |

Handlers use the client id returned from auth, not a re-read of the path, before
any metering call.

| Principal | Credential | May read |
|---|---|---|
| End-user (JWT) | Auth0 access token (`Bearer`) | Own actor rows for that app |
| End-user (`sk_*`) | API key (`Bearer`, Railway only) | Own actor rows for that app |

Gateway accepts Auth0 JWT / DCR Bearer only. See [DEV-PORTAL.md](DEV-PORTAL.md)
and [USAGE.md](USAGE.md).

### User-provision route (`POST …/users`)

Still HTTP Basic via `authorizeTenant` / `tenantauth`:

| Principal | Credential | May address |
|---|---|---|
| Platform admin | `AUTH0_SIGNER_M2M_CLIENT_ID` / secret | any tenant |
| Tenant admin | `clientId` / secret from `TENANT_ADMIN_KEYS` | own `clientId` only |

Secrets are compared with `crypto/subtle`. When platform credentials are unset
the platform path is disabled rather than matching on empty input.

### Deliberate response choices

- **Cross-app access returns `404`, not `403`.** A `403` would confirm that
  another app's client id exists.
- **`externalUserId` on usage** must be omitted or equal the actor; `:` is
  rejected with `400`.
- **An empty `client_id` filter returns no rows** rather than querying the meter
  unscoped.
- **Public access/grants HTTP routes are removed.** Allowance remains on session
  provision / token exchange.
---

## Manual prerequisite steps

These cannot be automated and must be done before the admin API is useful.
Steps 1–3 are one-time per deployment; step 4 is per tenant.

### 1. Create the Konnect organization — manual, console only

Konnect exposes no public Create-Organization API. Create **one** organization
for the deployment at <https://cloud.konghq.com>, and note its region (`us` or
`eu`); it determines the API host, e.g.
`https://us.api.konghq.com/v3/openmeter`.

Do not create one per tenant. That is the model this design deliberately does
not use, and mixing the two splits usage across orgs the admin API cannot join.

### 2. Create the platform system account and token — manual, console only

In **Organization → System Accounts**, create one account (e.g.
`clearinghouse-platform`) and assign these Metering roles:

| Role | Needed for |
|---|---|
| Metering Admin | meter and feature provisioning |
| Product Catalog Admin | plans and rate cards |
| Billing Admin | customers, subscriptions, credit grants |
| Metering Viewer | usage queries |

Issue a **System Account Access Token** and record it. It is shown once.

> This token can read every customer in the org. It belongs only in the
> clearinghouse's environment — never in a tenant's hands, a client bundle, or
> a browser. It is the single credential the whole boundary rests on.

### 3. Provision the catalog

```bash
export KONGCTL_DEFAULT_KONNECT_PAT="kpat_…"   # or OPENMETER_API_KEY
export OPENMETER_URL="https://us.api.konghq.com/v3/openmeter"
./openmeter-collector/provision/bootstrap.sh catalog
```

Idempotent — it creates only what is missing. Meters are immutable in
OpenMeter, so it never updates or deletes them. `bootstrap.ps1` is the
PowerShell equivalent.

### 4. Issue tenant admin credentials — per tenant (user provisioning)

Generate a high-entropy secret per tenant and add it to `TENANT_ADMIN_KEYS`, a
JSON object mapping client id to secret. Used for `POST …/users` (not usage):

```bash
openssl rand -hex 32
TENANT_ADMIN_KEYS='{"app_abc123":"<secret>","app_def456":"<secret>"}'
```

```bash
curl -u "app_abc123:<secret>" -H 'Content-Type: application/json' \
  -d '{"externalUserId":"alice"}' \
  "https://…/api/v1/apps/app_abc123/users"
```

Usage authenticates with end-user Bearer JWT / `sk_*` — see [USAGE.md](USAGE.md).
Leaving `TENANT_ADMIN_KEYS` unset is valid: user provisioning then accepts the
platform credential only.

### 5. Configure the service

```bash
OPENMETER_URL=https://us.api.konghq.com/v3/openmeter
OPENMETER_API_KEY=kpat_…            # the step-2 token
AUTH0_SIGNER_M2M_CLIENT_ID=…        # platform admin principal (users / token exchange)
AUTH0_SIGNER_M2M_CLIENT_SECRET=…
TENANT_ADMIN_KEYS={"…":"…"}         # optional; user provisioning
REMOTE_SIGNER_WEBHOOK_URL=…         # identity-webhook for JWT verify on usage + token exchange
WEBHOOK_SECRET=…
```

If `OPENMETER_URL` or `OPENMETER_API_KEY` is unset the usage route answers
`503` rather than starting up in a state where it appears to work.

---

## Rotation and revocation

- **A tenant secret** — replace its entry in `TENANT_ADMIN_KEYS` and restart.
  Removing the entry revokes that tenant's admin access immediately; it does
  not affect metering, the collector, or the tenant's end users.
- **The platform token** — issue a new SPAT on the same system account, deploy
  it as `OPENMETER_API_KEY`, then delete the old token in the console. Both are
  valid during the overlap, so there is no ingest gap.
- **Compromise of the platform token** is the serious case: it can read every
  tenant. Delete it in the console first, then redeploy. Rotating tenant
  secrets alone does not contain it.

---

## What this does not cover

- **Rate limiting per tenant.** One tenant can exhaust shared OpenMeter quota.
  The subject and page caps bound a single request, not a request rate.
- **Audit logging of admin reads.** Queries are not currently attributed to the
  calling principal in a durable log.
- **Tenant-managed credential rotation.** Secrets are operator-managed via
  environment; there is no self-service rotation endpoint.
- **Per-tenant encryption at rest.** All tenants share OpenMeter's storage;
  isolation is logical, not cryptographic.
