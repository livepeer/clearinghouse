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
| **Konnect / OpenMeter** (external) | Hosted metering and billing API. Set `OPENMETER_INGEST_URL` to your ingest endpoint. | [Konnect OpenMeter](https://docs.konghq.com/konnect/openmeter/), [self-hosted OpenMeter](https://openmeter.io/docs/deploy/kubernetes) |

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

**Identity & auth.** The in-compose **identity-webhook** is self-contained: it implements go-livepeer's remote-signer webhook wire protocol in-repo (`identity-webhook/protocol.mjs`) and pluggable end-user verifiers (`identity-webhook/verifiers.mjs`) — an API-key verifier and an OAuth/OIDC verifier built on [`jose`](https://github.com/panva/jose). The signer container runs `go-livepeer` directly; every signing request is authorized by go-livepeer's `-remoteSignerWebhookUrl` hook, which calls `/authorize` with `Authorization: Bearer <WEBHOOK_SECRET>`. End users present their credential to the signer — `Authorization: Bearer sk_…` (API key) or `Authorization: Bearer <jwt>` (OIDC) — and the webhook resolves it to `auth_id = "{client_id}:{usage_subject}"`. Set `OIDC_ISSUER`/`OIDC_AUDIENCE` to enable bring-your-own-OAuth (JWTs verified against your IdP's JWKS); configure both API keys and OIDC to accept either. For local alive checks only, leave `REMOTE_SIGNER_WEBHOOK_URL` empty to omit the webhook hook.

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
# For a local alive check without an identity webhook:
#   REMOTE_SIGNER_WEBHOOK_URL=
#   WEBHOOK_SECRET=

docker compose up -d --build kafka identity-webhook remote-signer
docker compose logs -f remote-signer
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
# kafka "healthy", identity-webhook "healthy" (signer waits for both), remote-signer "Up"

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

## Environment variables

All variables are documented in [`.env.example`](.env.example), grouped by service:

| Service | Key variables |
| --- | --- |
| `identity-webhook` | `WEBHOOK_SECRET`, `IDENTITY_ISSUER`, `DEMO_API_KEY`, `DEMO_CLIENT_ID`, `DEMO_USER_ID`, `API_KEY_PREFIX` (optional, default `sk_`), `OIDC_*` (optional) |
| `kafka` | `KAFKA_ADVERTISED_ADDR` |
| `remote-signer` | `REMOTE_SIGNER_WEBHOOK_URL`, `WEBHOOK_SECRET`, `SIGNER_*`, `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC` |
| `openmeter-collector` | `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC`, `OPENMETER_INGEST_URL`, `OPENMETER_API_KEY`, `PRICE_ORACLE_URL`, `PRICE_ORACLE_REFRESH` |

Shared keys (`WEBHOOK_SECRET`, `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC`) are listed once at the top of `.env.example`.

Signer state (keystore, `.eth-password`, chain DB) is stored under [`remote-signer/data/`](remote-signer/data/), bind-mounted to `/data` in the container.

```bash
mkdir -p remote-signer/data/keystore
cp /path/to/your/keystore/* remote-signer/data/keystore/
cp /path/to/your/.eth-password remote-signer/data/.eth-password

$EDITOR .env
```

Set `SIGNER_ETH_KEYSTORE_PATH=/data/keystore` (container path) and `SIGNER_ETH_ADDR` to your funded signer address. If `SIGNER_ETH_KEYSTORE_PATH` is unset, the entrypoint uses `/data/keystore` when that directory exists.

To change the host signing port or bind on all interfaces, use a Compose override file (see **Signing port loopback-only by default** under Design decisions).

## OpenMeter/Konnect bootstrap

Provision meters, features, and the default pay-per-use plan before starting the collector.
Use the Go `clearinghouse-bootstrap` CLI or your existing Konnect setup.

Creates:

| Object | Key | Purpose |
| --- | --- | --- |
| Meter | `network_fee_usd_micros` | Raw network cost from signer |
| Meter | `billable_usd_micros` | Post-markup billable amount (collector phase 2) |
| Meter | `signed_ticket_count` | Request counts |
| Feature | `network_spend` | Trial/network spend feature |
| Feature | `billable_spend` | Billable usage feature |
| Plan | `clearinghouse_default_ppu` | Pay-per-use rate card |

Idempotent — safe to re-run.

### Two-meter billing model

```text
Signer computed_fee (wei)
  → collector: network_fee_usd_micros   (raw network cost — observability)
  → collector: billable_usd_micros      (network × pipeline/model markup — billing)
       → billable_spend feature
            → clearinghouse_default_ppu subscription per customer
```

Collector pipeline config: [`openmeter-collector/collector.yaml`](openmeter-collector/collector.yaml).
The collector emits `billable_usd_micros` as an interim passthrough equal to
`network_fee_usd_micros` so the billable meter validates and accumulates. Phase-2
markup rules (network × pipeline/model multiplier) are not applied yet — until then
`billable_usd_micros == network_fee_usd_micros`.

### Identity contract (collector)

Upstream Kafka events carry `auth_id` as `client_id:usage_subject` (webhook → go-livepeer state → Kafka; unchanged).

The collector parses `auth_id` once (first-colon split) and emits normalized CloudEvents to Konnect/OpenMeter:

- `subject` = end user (`usage_subject`)
- `data.client_id` = tenant
- `data.usage_subject` = end user
- `data.auth_id` retained for compatibility; `data.external_user_id` mirrors `usage_subject` for meter `groupBy`

Demo API key defaults: `sk_demo_local_key` → `demo-client:demo-user` (configured in `.env`).
