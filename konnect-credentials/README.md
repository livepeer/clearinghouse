# Konnect credentials service

Minimal clearinghouse API that **binds a per-tenant Konnect organization** and
issues two SPATs for the tenant (Provisioner + Usage), while retaining an
**Ingest** SPAT for the openmeter-collector. Tenants call the Konnect Metering &
Billing API directly — this service does **not** mirror OpenMeter.

## Why per-tenant orgs?

Konnect Metering roles (`Metering Viewer`, `Billing Admin`, …) are **org-wide**.
A SPAT in a shared org can see every customer. Isolation requires one Konnect
org per platform `client_id`.

Konnect has **no public Create Organization API**. Tenants (or platform ops)
create the org out-of-band, then **bind** it here with an admin PAT.

## Access levels

| Credential | Roles (`entity_type_name: Metering`) | Holder |
| --- | --- | --- |
| Provisioner SPAT | Billing Admin, Product Catalog Admin, Metering Admin | Tenant |
| Usage SPAT | Metering Viewer, Billing Viewer | Tenant |
| Ingest SPAT | Ingest | Clearinghouse only |

## API

All routes (except `/health`) require `Authorization: Bearer <PLATFORM_API_SECRET>`
or `x-api-key: <PLATFORM_API_SECRET>`.

### Bind org

```http
POST /v1/tenants/{clientId}/konnect/bind
Content-Type: application/json

{ "region": "us", "admin_token": "kpat_…" }
```

Validates the token via `GET /organizations/me`, stores encrypted admin token.

### Issue SPATs

```http
POST /v1/tenants/{clientId}/konnect/credentials
```

Creates `ch-provisioner`, `ch-usage`, `ch-ingest` system accounts, assigns roles,
mints SPATs. **Provisioner and Usage secrets are returned once.** Ingest is stored
encrypted for the collector and is not returned.

### Bootstrap catalog

```http
POST /v1/tenants/{clientId}/konnect/catalog
```

Idempotently creates meters / features / default plan from `catalog.json`
(same catalog as `openmeter-collector/provision`).

### Rotate / revoke

```http
POST /v1/tenants/{clientId}/konnect/credentials/rotate
{ "kind": "provisioner" | "usage" | "ingest" }

DELETE /v1/tenants/{clientId}/konnect/credentials/{kind}
```

### Collector ingest lookup

```http
GET /v1/internal/tenants/{clientId}/ingest
```

Returns `{ url, token, region, org_id }` for CloudEvent POST.

## Env

| Variable | Purpose |
| --- | --- |
| `PLATFORM_API_SECRET` | M2M auth for this API |
| `CREDENTIALS_ENCRYPTION_KEY` | AES key (64-hex, 32-byte base64, or passphrase) |
| `DATA_DIR` | Tenant JSON store (default `./data`) |
| `PORT` | Listen port (default `8091`) |
| `CATALOG_PATH` | Optional override for catalog.json |
| `KONNECT_IDENTITY_BASE` | Default `https://global.api.konghq.com/v2` |

## Local

```bash
export PLATFORM_API_SECRET=dev-platform-secret
export CREDENTIALS_ENCRYPTION_KEY=dev-encryption-passphrase
node server.mjs
```

Tests:

```bash
npm test
```

## BYO org bind flow

1. Tenant creates a Konnect org (signup UI) in the desired region.
2. Tenant generates an org admin PAT (`kpat_…`) with Metering & Billing access.
3. Platform calls `POST …/konnect/bind` with that PAT.
4. Platform calls `POST …/konnect/credentials` and delivers Provisioner + Usage SPATs to the tenant (store securely; shown once).
5. Platform calls `POST …/konnect/catalog`.
6. Tenant uses SPATs against `https://{region}.api.konghq.com/v3/openmeter/*`.
7. Collector resolves ingest via `GET …/internal/tenants/{clientId}/ingest`.
