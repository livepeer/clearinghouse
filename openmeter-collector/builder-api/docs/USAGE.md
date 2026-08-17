# Querying usage (builder-api admin surface)

How app operators and platform integrators read metered usage and allowance
against the **live builder-api**, without holding a Kong Konnect SPAT.

Interactive OpenAPI: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

For the isolation model and operator prerequisites (org, SPAT, catalog,
`TENANT_ADMIN_KEYS`), see [TENANT-ISOLATION.md](TENANT-ISOLATION.md).

---

## Why not call Kong directly?

Clearinghouse runs **one** shared Konnect Metering & Billing organization.
A system-account token in that org can read **every** customer. Tenants never
receive that credential.

All tenant-facing reads go through builder-api admin routes, which:

1. Authenticate the caller (platform M2M or per-tenant admin).
2. Authorize the path `clientId`.
3. Build OpenMeter subjects from the **authorized** client id only.
4. Filter response rows again as defence in depth.

Raw Konnect `POST /v3/openmeter/meters/{meterId}/query` remains a
**platform-operator** tool for debugging, not an integrator API.

Konnect Gateway / Auth0 OIDC in front of these routes is a follow-up once the
ingest stack is live and external callers need a public edge. Until then,
treat builder-api Basic auth as the contract.

---

## Subjects

| Shape | Example | Where it appears |
| --- | --- | --- |
| Compound customer key | `{clientId}:{externalUserId}` | Usage subjects, access `customerKey`, CloudEvent `subject` from the collector |
| Owner (bare UUID) | `2e51154b-d296-4015-990c-02d5f16ecf1e` | OpenMeter customer for app-owner starter plans — **not** addressable under `apps/{clientId}/usage` |

`externalUserId` must not contain `:`. A colon would make the compound key
ambiguous; the API rejects it with `400`.

---

## Authentication

HTTP Basic on every admin route.

| Principal | Username | Password | May address |
| --- | --- | --- | --- |
| Platform admin | `AUTH0_SIGNER_M2M_CLIENT_ID` | `AUTH0_SIGNER_M2M_CLIENT_SECRET` | Any `clientId` |
| Tenant admin | that app's `clientId` | secret from `TENANT_ADMIN_KEYS` | Own `clientId` only |

Cross-tenant attempts return **`404` `app not found`**, not `403`, so the API
does not confirm whether another client id exists.

```bash
export BUILDER_API=https://builder-api-production-82bf.up.railway.app
export CLIENT_ID=xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx   # Demo App public client id

# Platform
curl -u "$AUTH0_SIGNER_M2M_CLIENT_ID:$AUTH0_SIGNER_M2M_CLIENT_SECRET" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros"

# Tenant
curl -u "$CLIENT_ID:$TENANT_SECRET" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros"
```

---

## Endpoints

### Usage — `GET /api/v1/apps/{clientId}/usage`

| Query | Required | Notes |
| --- | --- | --- |
| `meter` | yes | Catalog meter **key** (see below) |
| `externalUserId` | no | One subject; omit for tenant-wide (bounded customer scan) |
| `from` / `to` | no | RFC3339 window |
| `groupBy` | no | Extra group-by dimensions (subject is always included server-side) |

**Tenant-wide** lists customer keys prefixed `{clientId}:` (capped), then
queries that subject set. **Per-user** queries exactly
`{clientId}:{externalUserId}`.

Empty `rows` with a non-empty `subjects` list means the customer(s) exist but
no events have been ingested yet for that meter/window — not an auth failure.

```bash
# Tenant-wide
curl -sS -u "$CLIENT_ID:$TENANT_SECRET" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros" | jq .

# One user
curl -sS -u "$CLIENT_ID:$TENANT_SECRET" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros&externalUserId=demo-user" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "meter": "billable_usd_micros",
  "subjects": [
    "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:alice",
    "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:demo-user"
  ],
  "rows": []
}
```

### Access — `GET /api/v1/apps/{clientId}/users/{externalUserId}/access`

Optional `?feature=` (defaults to `OPENMETER_TRIAL_FEATURE_KEY`). Resolves the
compound key to the Konnect customer **ULID**, then reads credits balance (then
entitlement-access). Unknown customers → `404`.

```bash
curl -sS -u "$CLIENT_ID:$TENANT_SECRET" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/users/demo-user/access" | jq .
```

### Grants — `POST /api/v1/apps/{clientId}/users/{externalUserId}/grants`

```bash
curl -sS -u "$CLIENT_ID:$TENANT_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"amountUsdMicros":5000000,"grantKey":"ops-topup-1"}' \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/users/demo-user/grants" | jq .
```

`amountUsdMicros` is a JSON **number**. Optional `featureKey`, `grantKey`
(default `admin-grant`).

---

## Catalog meter keys

Provisioned by `openmeter-collector/provision/bootstrap.sh catalog`:

| Key | Role |
| --- | --- |
| `network_fee_usd_micros` | Settlement / observability network fee |
| `billable_usd_micros` | Post-markup billable amount |
| `signed_ticket_count` | Ticket count |
| `fee_wei` | Fee in wei |
| `billable_secs` | Billable seconds |
| `network_fee_usd_micros_by_manifest` | Network fee grouped by manifest |

Pass the **key** as `meter=` on the usage route. Builder-api resolves it to the
Konnect meter ULID before querying.

---

## Status codes worth knowing

| Code | Meaning |
| --- | --- |
| `401` | Missing / wrong Basic credentials |
| `404` | Unknown app for this principal, or unknown customer on access/grant |
| `400` | Bad `externalUserId` (e.g. contains `:`) or invalid grant body |
| `502` | Konnect/OpenMeter call failed |
| `503` | Metering backend not configured (`OPENMETER_API_KEY` unset) |

---

## Design notes

- **Key vs id.** Usage subjects are customer **keys**. Credits and entitlements
  are addressed by Konnect customer **id**. Admin access/grant handlers resolve
  key → id before calling OpenMeter; do not substitute the compound key into
  `/customers/{id}/…` paths yourself.
- **Bounded scans.** Tenant-wide usage walks at most `maxCustomerPages` (50) ×
  `pageSize` (100) customers and at most `maxUsageSubjects` (500) subjects per
  request. Large tenants need per-user queries or pagination elsewhere.
- **Empty subject list.** If the tenant has no customers, the handler returns
  `subjects: []` / `rows: []` and does **not** run an unscoped meter query.

---

## Implementation tasks

- [ ] Keep this guide aligned with `/api/v1/openapi.json` when routes change.
- [ ] Add production `BUILDER_API` URL to deploy docs once domains stabilize.
- [ ] Document Gateway + Auth0 OIDC (Part C) when usage is exposed on the public
      internet; leave `/authorize` and token exchange internal.
- [ ] Link sample SDK snippets once the TypeScript client consumes these routes.
