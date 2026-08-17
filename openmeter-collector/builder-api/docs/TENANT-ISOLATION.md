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

Four layers, each of which independently prevents cross-tenant reads. They are
listed in the order a request encounters them.

| # | Layer | Where | What it stops |
|---|---|---|---|
| 1 | Principal authentication | `internal/tenantauth` | Unknown or mismatched credentials become an unauthenticated principal that can reach nothing |
| 2 | Path authorization | `Server.authorizeTenant` | A tenant principal addressing another tenant's `clientId` |
| 3 | Scope construction | `handleUsage`, `handleUserAccess` | Subjects are built from the **authorized** client id, never from caller input |
| 4 | Response filtering | `filterRowsToTenant` | A metering backend that ignores the subject filter and returns the whole meter |

Layer 3 is the one that matters most and the easiest to regress. Handlers take
the client id from `authorizeTenant`'s **return value**, not by re-reading
`r.PathValue("clientId")`. An authorized tenant id is therefore the only thing
that can reach the metering layer.

### Principals

| Principal | Credential | May address |
|---|---|---|
| Platform admin | `AUTH0_SIGNER_M2M_CLIENT_ID` / `AUTH0_SIGNER_M2M_CLIENT_SECRET` | any tenant |
| Tenant admin | `clientId` / its secret from `TENANT_ADMIN_KEYS` | its own `clientId` only |

Both are HTTP Basic. Secrets are compared with `crypto/subtle`. When platform
credentials are unset the platform path is disabled rather than matching on
empty input, so a partially configured deployment fails closed.

### Deliberate response choices

- **Cross-tenant access returns `404`, not `403`.** A `403` would confirm that
  another tenant's client id exists. Unknown app and forbidden app are
  indistinguishable from outside.
- **`externalUserId` may not contain `:`.** It becomes the second half of a
  customer key; a colon would make the key ambiguous about where the tenant
  ends. Rejected with `400` on both the path segment and the query parameter.
- **Prefix matching includes the separator.** Client id `acme` matches
  `acme:alice` and never `acme-corp:eve`.
- **An empty subject list returns no rows** rather than querying the meter
  unscoped, which on a shared tenant would return everyone's usage.
- **Tenant-wide queries are bounded** — `maxUsageSubjects` (500) per request and
  `maxCustomerPages` (50) on the customer scan.

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

### 4. Issue tenant admin credentials — per tenant

Generate a high-entropy secret per tenant and add it to `TENANT_ADMIN_KEYS`, a
JSON object mapping client id to secret:

```bash
openssl rand -hex 32
```

```bash
TENANT_ADMIN_KEYS='{"app_abc123":"<secret>","app_def456":"<secret>"}'
```

The tenant authenticates with HTTP Basic, username = its `clientId`:

```bash
curl -u "app_abc123:<secret>" \
  "https://…/api/v1/apps/app_abc123/usage?meter=billable_usd_micros"
```

Leaving `TENANT_ADMIN_KEYS` unset is valid: the admin routes then accept the
platform credential only, which is the right posture for a single-tenant or
local deployment.

### 5. Configure the service

```bash
OPENMETER_URL=https://us.api.konghq.com/v3/openmeter
OPENMETER_API_KEY=kpat_…            # the step-2 token
AUTH0_SIGNER_M2M_CLIENT_ID=…        # platform admin principal
AUTH0_SIGNER_M2M_CLIENT_SECRET=…
TENANT_ADMIN_KEYS={"…":"…"}         # optional
```

If `OPENMETER_URL` or `OPENMETER_API_KEY` is unset the admin routes answer
`503` rather than starting up in a state where they appear to work.

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
