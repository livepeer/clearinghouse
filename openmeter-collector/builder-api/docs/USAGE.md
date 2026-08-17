# Querying usage (builder-api)

How end-users and app integrators read **their own** metered usage against
OpenMeter through builder-api — without holding a Kong Konnect SPAT.

Interactive OpenAPI: `GET /api/v1/docs` (spec at `/api/v1/openapi.json`).

For the isolation model see [TENANT-ISOLATION.md](TENANT-ISOLATION.md).
For Dev Portal + Kong Gateway (Auth0 OIDC at the edge) see
[DEV-PORTAL.md](DEV-PORTAL.md).

---

## Why not call Kong metering directly?

Clearinghouse runs **one** shared Konnect Metering & Billing organization.
A system-account token in that org can read **every** customer. Tenants never
receive that credential.

Self-serve usage goes through `GET /api/v1/apps/{clientId}/usage`, which:

1. Authenticates Bearer Auth0 user JWT or `sk_*`.
2. Requires path `clientId` to match the credential’s app.
3. Queries OpenMeter with `filters.dimensions.client_id`.
4. Keeps only rows whose `external_user_id` matches the actor (defense in depth).

Public HTTP access/grants routes are **not** exposed. Allowance continues to be
enforced on token exchange / session provision.

---

## Credential split

| Credential | Railway (direct builder-api) | Kong Gateway (`$KONNECT_PROXY_URL`) |
| --- | --- | --- |
| Auth0 user JWT / DCR Bearer | Yes | Yes (OIDC plugin validates first) |
| `sk_*` API key | Yes | **No** — OIDC does not understand `sk_*`; no Key Auth in v1 |
| Tenant / platform HTTP Basic | **No** on usage (401) | **No** |

M2M Basic remains valid for `POST …/users` and token exchange — not for usage.

`/authorize` (identity-webhook) and `POST …/oidc/token` stay **off** the public
gateway.

---

## Contract

```
GET /api/v1/apps/{clientId}/usage?meter=&from=&to=[&externalUserId=][&groupBy=]
Authorization: Bearer <Auth0 user JWT | sk_* on Railway only>
```

| Query | Required | Notes |
| --- | --- | --- |
| `meter` | yes | Catalog meter **key** only |
| `externalUserId` | no | Omit, or must equal the authenticated actor |
| `from` / `to` | no | RFC3339 window |
| `groupBy` | no | Catalog dimensions only; `client_id` + `external_user_id` always included |

Path `clientId` must match the credential app id → else **404**.
Unknown / non-catalog meter → **400**.
Basic auth on this route → **401**.

---

## Examples

```bash
export BUILDER_API=https://builder-api-production-82bf.up.railway.app
export CLIENT_ID=xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx
export TOKEN=…   # Auth0 user access token, or sk_* for Railway

# Railway direct (JWT or sk_*)
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$BUILDER_API/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros" | jq .

# Gateway (JWT / DCR Bearer only)
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$KONNECT_PROXY_URL/api/v1/apps/$CLIENT_ID/usage?meter=billable_usd_micros" | jq .
```

Example shape:

```json
{
  "clientId": "xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx",
  "meter": "billable_usd_micros",
  "actor": "alice",
  "subjects": ["xEJfZBtEP0JLJtlXm9UnJrDrA9bwepLx:alice"],
  "rows": []
}
```

Empty `rows` means no ingested events for that actor/meter/window — not an auth
failure.

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

Builder-api resolves the key to the Konnect meter ULID before querying.

---

## Status codes

| Code | Meaning |
| --- | --- |
| `401` | Missing / invalid Bearer (including Basic on this route) |
| `404` | Path `clientId` does not match the credential app |
| `400` | Bad meter, `groupBy`, window, or `externalUserId` ≠ actor |
| `502` | Konnect/OpenMeter call failed |
| `503` | Metering backend not configured |

---

## Design notes

- **Dimension filter, not subject scan.** Usage no longer walks customers by
  compound key prefix. Konnect is queried with `filters.dimensions.client_id`.
- **Actor match keys.** Rows may carry `external_user_id` as bare id,
  `owner:{id}`, or `user:{id}`. All variants belonging to the actor are kept.
- **Compound customer keys** (`{clientId}:{externalUserId}`) remain the
  OpenMeter customer key and CloudEvent subject for ingest/session provision.

---

## Implementation tasks

- [ ] Keep this guide aligned with `/api/v1/openapi.json` when routes change.
- [x] OpenAPI `servers` default to Railway; swap primary to `$KONNECT_PROXY_URL`
      once the gateway hostname is stable.
- [ ] Link sample SDK snippets once a client consumes this route.
