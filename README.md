# clearinghouse

Docker Compose stack for the clearinghouse runtime:
**identity-webhook → Redpanda → go-livepeer remote signer → OpenMeter/Benthos collector → Konnect metering**.

## Components

| Service | Role | Docs |
| --- | --- | --- |
| **identity-webhook** (`identity-webhook`) | Resolves end-user credentials (API keys and/or OAuth/OIDC JWTs) to `auth_id` for go-livepeer's `/authorize` hook. Self-contained: implements the go-livepeer webhook wire protocol in-repo, verifying JWTs with `jose`. | [jose](https://github.com/panva/jose) |
| **Redpanda** (`kafka`) | Kafka-compatible event bus. The signer publishes gateway events; the collector consumes them. | [Redpanda docs](https://docs.redpanda.com/) |
| **go-livepeer remote signer** (`remote-signer`) | Signs Livepeer payment tickets and emits `create_signed_ticket` events to Kafka. | [go-livepeer](https://github.com/livepeer/go-livepeer) |
| **OpenMeter collector** (`openmeter-collector`) | Benthos pipeline: filters Kafka events, converts fees to USD micros, POSTs CloudEvents to OpenMeter ingest. | [OpenMeter collector](https://openmeter.io/docs/collectors) |
| **Konnect / OpenMeter** (external) | Hosted metering and billing API. Set `OPENMETER_URL` to your OpenMeter API base; the collector appends the events path. | [Konnect OpenMeter](https://docs.konghq.com/konnect/openmeter/), [self-hosted OpenMeter](https://openmeter.io/docs/deploy/kubernetes) |

Data flow:

```text
Signer HTTP request
  → identity webhook (/authorize)
  → signed ticket + Kafka create_signed_ticket event
  → collector transforms event
  → OpenMeter ingest API
```

## Design decisions

**Redpanda over Apache Kafka.** The stack uses Redpanda as the Kafka-compatible broker. Redpanda runs as a single-binary dev container with no ZooKeeper dependency and faster local startup.

**Identity & auth.** The in-compose **identity-webhook** is self-contained: it implements go-livepeer's remote-signer webhook wire protocol in-repo (`identity-webhook/protocol.mjs`) and pluggable end-user verifiers (`identity-webhook/verifiers.mjs`) — an API-key verifier and an OAuth/OIDC verifier built on [`jose`](https://github.com/panva/jose). Set `IDENTITY_AUTH_MODE` to `api_key` or `oidc` to select exactly one verifier (no fallback). The signer container runs `go-livepeer` directly; every signing request is authorized by go-livepeer's `-remoteSignerWebhookUrl` hook, which calls `/authorize` with `Authorization: Bearer <WEBHOOK_SECRET>`. End users present their credential to the signer — `Authorization: Bearer sk_…` (API key mode) or `Authorization: Bearer <jwt>` (OIDC mode) — and the webhook resolves it to `auth_id = "{client_id}:{usage_subject}"`. For local alive checks only, leave `REMOTE_SIGNER_WEBHOOK_URL` empty to omit the webhook hook.

**CLI port not exposed.** go-livepeer's `-cliAddr` (admin/RPC) is bound to `127.0.0.1:4935` inside the container and is never published or mapped to the host.

**Signing port loopback-only by default.** Compose publishes the signing HTTP port as `127.0.0.1:8081` so an accidentally unauthenticated signer (when `REMOTE_SIGNER_WEBHOOK_URL` is empty) is not reachable from the LAN. To expose on all interfaces — e.g. for a gateway on another host — add a Compose override:

```yaml
# docker-compose.override.yml
services:
  remote-signer:
    ports:
      - "8081:8081"
```

Only bind `0.0.0.0` when `REMOTE_SIGNER_WEBHOOK_URL` and `WEBHOOK_SECRET` are set; an open signer can drain deposits.

**Stack configuration.** Copy [`.env.example`](.env.example) to `.env` at the repo root before starting. All Compose services read from that file — kafka, remote-signer, and openmeter-collector mount it at `/service/.env` and source it in the entrypoint; identity-webhook reads it via Compose `env_file`.

## Local stack

### 1. Quick check — Kafka + identity webhook + signer

Start here before wiring metering. This runs the broker, identity webhook, and remote signer.

```bash
cp .env.example .env
$EDITOR .env

docker compose up -d --build kafka identity-webhook remote-signer
docker compose logs -f remote-signer
```

Signer-only alive check (no identity webhook — omit the service and clear the signer hook URL; leave `WEBHOOK_SECRET` and other identity-webhook vars unchanged in `.env`):

```bash
# In .env: REMOTE_SIGNER_WEBHOOK_URL=
docker compose up -d --build kafka remote-signer
```

Verify the identity webhook (simulates go-livepeer calling `/authorize`; secret matches `.env.example`):

```bash
docker compose exec identity-webhook \
  curl -sS -X POST http://localhost:8090/authorize \
    -H "Authorization: Bearer dev-webhook-secret-change-me" \
    -H "Content-Type: application/json" \
    -d '{"headers":{"Authorization":["Bearer sk_demo_local_key"]}}'
# expected: "status":200, "auth_id":"demo-client:demo-user"
```

Expected result: `remote-signer` starts cleanly, connects to Kafka, and serves the signing HTTP port.

Smoketests:

```bash
docker compose ps
# kafka "healthy"; with identity-webhook started, it should also be "healthy"; remote-signer "Up"

curl -fsS -X POST http://localhost:8081/sign-orchestrator-info
# {"address":"0x…","signature":"0x…"} — keystore unlocked, signer can sign
```

Verify CLI port is not published:

```bash
docker compose port remote-signer 4935
# expected: no output / error (port is not mapped)
docker compose port remote-signer 8081
# expected: 127.0.0.1:8081
```

### 2. Full stack — add metering

After the quick check passes, add the OpenMeter collector and hosted metering configuration. Provision OpenMeter meters/features (see [OpenMeter/Konnect bootstrap](#openmeterkonnect-bootstrap)), then set `OPENMETER_API_KEY` in `.env`:

```bash
$EDITOR .env

docker compose up -d --build
docker compose logs -f
docker compose down
```

Smoketest — produce a signed-ticket event; the collector forwards it to OpenMeter/Konnect:

```bash
docker compose exec -T kafka rpk topic create livepeer-gateway-events
# gateway topic (broker auto-create is off)

echo '{"type":"create_signed_ticket","data":{"auth_id":"demo-client:demo-user","computed_fee":"1000000000000000","request_id":"clearinghouse-smoketest","pipeline":"live-video-to-video","pixels":"1000"}}' \
  | docker compose exec -T kafka rpk topic produce livepeer-gateway-events
# collector consumes it, converts the fee, POSTs to OpenMeter/Konnect

docker compose logs --tail=20 openmeter-collector
# no ERROR = forwarded to OpenMeter
```

Re-runs are dedup-safe (OpenMeter deduplicates by event id). A real signer-emitted event needs a full gateway or [local SDK](https://github.com/livepeer/livepeer-python-gateway) to call a real job with a funded signer — out of scope here.

### 3. Local smoke test (mock profile)

The `mock` Compose profile starts a tiny HTTP server that stands in for the external services. **Opt in only** — it is not started by default. It exposes three endpoints:

| Endpoint | Mocks | Behaviour |
| --- | --- | --- |
| `POST /authorize` | identity webhook | Validates `Authorization: Bearer <WEBHOOK_SECRET>` and returns `{"status":200,"auth_id":"<MOCK_AUTH_ID>"}`. go-livepeer stamps the returned `auth_id` (`client_id:usage_subject`) onto the Kafka event it emits. |
| `POST /events` | OpenMeter/Konnect ingest | Logs the CloudEvents payload and returns `204`. |
| `GET /prices` | ETH/USD price oracle | Returns a Coinbase-shaped `{"data":{"amount":"<MOCK_ETH_USD>"}}` so the collector can warm its price cache offline. |

`MOCK_AUTH_ID` (default `demo-client:demo-user`) and `MOCK_ETH_USD` (default `3500.00`) are set on the `mock-services` service in [`docker-compose.yml`](docker-compose.yml).

Set these values when using the mock:

```bash
# remote-signer/.env
REMOTE_SIGNER_WEBHOOK_URL=http://mock-services:8080/authorize
WEBHOOK_SECRET=dev-secret

# openmeter-collector/.env
OPENMETER_INGEST_URL=http://mock-services:8080/events
OPENMETER_API_KEY=mock-local-key
# With the price-oracle collector, point the oracle at the mock for a fully
# offline run (replaces the static ETH_USD_PRICE):
PRICE_ORACLE_URL=http://mock-services:8080/prices
```

Start Kafka, mock services, and the collector:

```bash
docker compose --profile mock up -d --build kafka mock-services openmeter-collector
```

Produce a sample `create_signed_ticket` event (matches [`openmeter-collector/collector.yaml`](openmeter-collector/collector.yaml) expectations):

```bash
docker compose exec kafka \
  rpk topic produce livepeer-gateway-events --format '%v' <<'EOF'
{"type":"create_signed_ticket","data":{"auth_id":"demo-client:demo-user","computed_fee":"1000000000000000","request_id":"smoke-req-1","pipeline":"lv2v","model_id":"daydream-video","pixels":"1920","current_time":"2026-06-24T12:00:00Z"}}
EOF
```

Confirm the mock received ingest traffic:

```bash
docker compose --profile mock logs mock-services
# expected: mock-services: POST /events ... ingest payload=...
```

With the normalized CloudEvents collector, the logged ingest payload carries
`subject` = `usage_subject` (end user) and `data.client_id` / `data.usage_subject`
/ `data.usage_subject_type` alongside `data.auth_id`.

## Environment variables

Canonical copy with inline comments: [`.env.example`](.env.example). Summary by service:

| Service | Key variables |
| --- | --- |
| `identity-webhook` | [`identity-webhook` variables](#identity-webhook) below |
| `kafka` | `KAFKA_ADVERTISED_ADDR` |
| `remote-signer` | `REMOTE_SIGNER_WEBHOOK_URL`, `WEBHOOK_SECRET`, `SIGNER_*`, `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC` |
| `openmeter-collector` | `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC`, `OPENMETER_URL`, `OPENMETER_API_KEY`, `PRICE_ORACLE_URL`, `PRICE_ORACLE_REFRESH` |

Shared keys (`WEBHOOK_SECRET`, `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC`) are listed once at the top of `.env.example`.

#### `identity-webhook`

| Variable | When | Notes |
| --- | --- | --- |
| `WEBHOOK_SECRET` | always | Shared with `remote-signer`; authenticates go-livepeer's `/authorize` caller |
| `IDENTITY_ISSUER` | always | Issuer stamped on resolved identities (e.g. `http://identity-webhook:8090`) |
| `IDENTITY_AUTH_MODE` | always | `api_key` or `oidc` — exactly one verifier, no fallback |
| `DEMO_API_KEY` | `api_key` | Primary demo key; Bearer token end users send to the signer |
| `DEMO_CLIENT_ID` | `api_key` | Tenant for `DEMO_API_KEY` (default `demo-client`) |
| `DEMO_USER_ID` | `api_key` | End user for `DEMO_API_KEY` (default `demo-user`) |
| `DEMO_API_KEYS` | `api_key`, optional | JSON map of extra keys, e.g. `{"sk_other":{"clientId":"app-b","userId":"user-b"}}` |
| `USAGE_SUBJECT_TYPE` | `api_key`, optional | Default `api_key_user`; stamped on API-key identities |
| `API_KEY_PREFIX` | `api_key`, optional | Default `sk_` |
| `OIDC_ISSUER` | `oidc` | JWT issuer / JWKS host (trailing slash and surrounding whitespace are normalized) |
| `OIDC_AUDIENCE` | `oidc` | Expected JWT audience |
| `OIDC_JWKS_URI` | `oidc`, optional | Explicit JWKS override; otherwise resolved via OIDC Discovery (`${OIDC_ISSUER}/.well-known/openid-configuration` → `jwks_uri`) |
| `OIDC_CLIENT_CLAIM` | `oidc`, optional | Tenant claim (default `azp`; Auth0 clearinghouse uses `app_client_id`) |
| `OIDC_SUBJECT_CLAIM` | `oidc`, optional | End-user claim (default `sub`; Auth0 clearinghouse uses `external_user_id`) |
| `OIDC_SUBJECT_TYPE` | `oidc`, optional | Default `oidc_user`; Auth0 clearinghouse uses `external_user_id` |
| `OIDC_REQUIRED_SCOPES` | `oidc`, optional | Space- or comma-separated required scopes (e.g. `sign:job`) |
| `OIDC_TOKEN_EXCHANGE_BASE_URL` | `oidc`, optional | Origin only (no path/query/hash/userinfo). HTTPS required except loopback (`localhost` / `127.0.0.1` / `::1`). Enables composite `Bearer app_<24hex>_<secret>`: RFC 8693 exchange at `/api/v1/apps/{clientId}/oidc/token`, then JWT verify (`RS256`/`ES256`, required `exp`, 60s clock skew) |
| `OIDC_EXCHANGE_M2M_CLIENT_ID` / `OIDC_EXCHANGE_M2M_CLIENT_SECRET` | `oidc`, optional | Optional client credentials for the exchange request (both or neither) |

OIDC JWT verification accepts only `RS256` and `ES256` (asymmetric algorithms). Tokens must include `exp` and the configured subject claim.

`PORT` is set by Compose (`8090`), not `.env`. See the `identity-webhook` block in [`.env.example`](.env.example) for Auth0 vs generic OIDC examples.

Signer state (keystore, `.eth-password`, chain DB) is stored under [`remote-signer/data/`](remote-signer/data/), bind-mounted to `/data` in the container.

```bash
mkdir -p remote-signer/data/keystore
cp /path/to/your/keystore/* remote-signer/data/keystore/
cp /path/to/your/.eth-password remote-signer/data/.eth-password

$EDITOR .env
```

Set `SIGNER_ETH_KEYSTORE_PATH=/data/keystore` (container path) and `SIGNER_ETH_ADDR` to your funded signer address. If `SIGNER_ETH_KEYSTORE_PATH` is unset, the entrypoint uses `/data/keystore` when that directory exists.

To change the host signing port or bind on all interfaces, use a Compose override file (see **Signing port loopback-only by default** under Design decisions).

## One-command bootstrap

```bash
./bootstrap.sh
```

Runs the Auth0 provisioner and the OpenMeter/Konnect catalog provisioner, then
emits `.env.livepeer` and `sdk-config.json` into
`auth0-provisioner/provision/`. Both provisioners are idempotent, so re-running
only fills in what is missing.

| Flag | Effect |
| --- | --- |
| `--app NAME` | Which app in `apps.json` to write `sdk-config.json` for (default: the first) |
| `--skip-auth0` | Reuse an existing `.env.livepeer` |
| `--skip-catalog` | Skip OpenMeter catalog provisioning |
| `--out DIR` | Where to write the artifacts |

Needs `jq`; Auth0 also needs the `auth0` CLI with an active `auth0 login`
session, and the catalog needs `kongctl` plus `OPENMETER_API_KEY` (or
`KONGCTL_DEFAULT_KONNECT_PAT`). The signer and webhook URLs baked into
`sdk-config.json` are deploy-time values — override `SIGNER_PROXY_URL`,
`SIGNER_PUBLIC_URL`, and `REMOTE_SIGNER_WEBHOOK_URL` once the platform is up.

The orchestrator does not reimplement either provisioner. The Auth0 definition
stays in `auth0-provisioner/provision/apps.json` and the catalog definition
stays in `openmeter-collector/provision/catalog.json`; each remains the single
source of truth for its half.

## OpenMeter/Konnect bootstrap

Run directly when you want the catalog without the Auth0 half, or need the
per-customer subcommands.
Use the [`kongctl` bootstrap scripts](openmeter-collector/provision/README.md) or your existing Konnect setup.

Aligned with pymthouse v0.3.3: Starter settles on **`network_spend`** with included
**`discounts.usage`** ($5 default) and **`credit_then_invoice`**. Prepaid credits are
manual top-ups only.

```bash
cd openmeter-collector/provision
./bootstrap.sh catalog
./bootstrap.sh customer demo-client demo-user "Demo User" --subscribe
./bootstrap.sh owner 2e51154b-d296-4015-990c-02d5f16ecf1e "App Owner" --subscribe
```

Creates:

| Object | Key | Purpose |
| --- | --- | --- |
| Meter | `network_fee_usd_micros` | Settlement + observability (network cost) |
| Meter | `billable_usd_micros` | Phase-2 markup (not on Starter plans) |
| Meter | `signed_ticket_count` | Request counts |
| Meter | `fee_wei` / `billable_secs` / `network_fee_usd_micros_by_manifest` | Analytics |
| Feature | `network_spend` | Starter settlement feature |
| Feature | `billable_spend` | Phase-2 billable feature |
| Plan | `clearinghouse_owner_starter` | Owner wallet Starter |
| Plan | `clearinghouse_demo_starter` | Demo M2M Starter |

Idempotent — safe to re-run.

### Settlement model

```text
Signer computed_fee (wei)
  → collector: network_fee_usd_micros   (exact fractional; settlement)
  → collector: billable_usd_micros      (interim = network; phase-2 markup later)
       → network_spend feature
            → Owner / Demo Starter subscription
                 discounts.usage (included $) then prepaid credits
```

Collector pipeline config: [`openmeter-collector/collector.yaml`](openmeter-collector/collector.yaml).
The collector emits `billable_usd_micros` as an interim passthrough equal to
`network_fee_usd_micros` so the billable meter validates and accumulates. Phase-2
markup rules (network × pipeline/model multiplier) are not applied yet — until then
`billable_usd_micros == network_fee_usd_micros`. Starter plans bill **`network_spend`**,
not `billable_spend`.

### Identity contract (collector)

Three layers — each owns a different piece of the identity story:

| Layer | What it defines | Role of `client_id:usage_subject` |
| --- | --- | --- |
| **go-livepeer** | Remote-signer webhook wire protocol ([PR #3897](https://github.com/livepeer/go-livepeer/pull/3897)): webhook returns an opaque `auth_id` string; signer stores it in payment state and copies it into Kafka `create_signed_ticket` events. | go-livepeer treats `auth_id` as an opaque string — it does not parse tenant vs end user. |
| **Clearinghouse** | Multi-tenant usage identity: `client_id` = tenant (developer app), `usage_subject` = end user within that tenant. The identity webhook joins them as `auth_id = "{client_id}:{usage_subject}"` ([`protocol.mjs`](identity-webhook/protocol.mjs)). App owners use wire `usage_subject = owner:{users.id}` as a transport marker. | This compound format is a clearinghouse convention so one shared signer can attribute usage across many platform apps and their users. |
| **OpenMeter / Konnect** | CloudEvent `subject` is the customer attribution key; each customer has `subject_keys` that must match incoming events exactly. | M2M customers are keyed by compound id (e.g. `demo-client:demo-user`). App owners share one customer keyed by bare `{users.id}`. The collector sets CloudEvent `subject` accordingly. |

Upstream Kafka events carry `auth_id` unchanged (`webhook → go-livepeer state → Kafka`). The collector parses it once (first-colon split) and emits normalized CloudEvents ([`collector.yaml`](openmeter-collector/collector.yaml)):

- **M2M / managed user:** `subject` = compound `auth_id` (`client_id:usage_subject`)
- **App owner:** wire `auth_id = client_id:owner:{users.id}` → `subject` = bare `{users.id}` (shared wallet); `usage_subject_type = app_owner`
- `data.client_id` = tenant (parsed from `auth_id`)
- `data.usage_subject` / `data.external_user_id` = end user id, or bare `{users.id}` for owners
- `data.auth_id` retained for compatibility; `data.openmeter_customer_key` = billing wallet key
- `data.eth_usd_price` = ETH/USD oracle rate (number) used for that event's Wei → USD micros conversion
- `network_fee_usd_micros` / `billable_usd_micros` are **exact fractional** values (`fee_wei * eth_usd / 1e12`) — no per-ticket ceil. OpenMeter SUM accumulates fractions; read paths / session boundaries ceil once so dense live tickets are not overbilled. Plan unit price remains $0.000001 per micro; fractional ingest is intentional.

Example egress event for M2M `auth_id = demo-client:demo-user`:

```json
{
  "subject": "demo-client:demo-user",
  "data": {
    "client_id": "demo-client",
    "usage_subject": "demo-user",
    "usage_subject_type": "external_user_id",
    "external_user_id": "demo-user",
    "auth_id": "demo-client:demo-user",
    "openmeter_customer_key": "demo-client:demo-user"
  }
}
```

Example egress event for app owner `auth_id = app_abc:owner:2e51154b-d296-4015-990c-02d5f16ecf1e`:

```json
{
  "subject": "2e51154b-d296-4015-990c-02d5f16ecf1e",
  "data": {
    "client_id": "app_abc",
    "usage_subject": "2e51154b-d296-4015-990c-02d5f16ecf1e",
    "usage_subject_type": "app_owner",
    "external_user_id": "2e51154b-d296-4015-990c-02d5f16ecf1e",
    "auth_id": "app_abc:owner:2e51154b-d296-4015-990c-02d5f16ecf1e",
    "openmeter_customer_key": "2e51154b-d296-4015-990c-02d5f16ecf1e"
  }
}
```

Demo API key defaults: `sk_demo_local_key` → `demo-client:demo-user` (configured in `.env`).
