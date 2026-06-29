# clearinghouse

Docker Compose stack for the clearinghouse runtime:
**identity-webhook → Redpanda → go-livepeer remote signer → OpenMeter/Benthos collector → Konnect metering**.

## Components

| Service | Role | Docs |
| --- | --- | --- |
| **identity-webhook** (`identity-webhook`) | Resolves end-user API keys to `auth_id` for go-livepeer's `/authorize` hook. Uses builder-sdk's API-key provider. | [builder-sdk](https://github.com/pymthouse/builder-sdk) |
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

**Identity & auth.** The in-compose **identity-webhook** uses builder-sdk's API-key provider. The signer container runs `go-livepeer` directly; every signing request is authorized by go-livepeer's `-remoteSignerWebhookUrl` hook, which calls `/authorize` with `Authorization: Bearer <WEBHOOK_SECRET>`. End users present `Authorization: Bearer sk_…` to the signer; the webhook resolves the key to `auth_id = "{client_id}:{usage_subject}"`. For local alive checks only, leave `REMOTE_SIGNER_WEBHOOK_URL` empty to omit the webhook hook.

**CLI port not exposed.** go-livepeer's `-cliAddr` (admin/RPC) is bound to `127.0.0.1:4935` inside the container and is never published or mapped to the host. Only the signing HTTP port (`8081`) is exposed.

**Per-service configuration.** Each service has a local `.env` file (copy from `.env.example` before starting). Kafka, remote-signer, and openmeter-collector mount theirs at `/service/.env` and source it in the entrypoint. identity-webhook reads its `.env` via Compose `env_file`.

## Local stack

### 1. Quick check — Kafka + identity webhook + signer

Start here before wiring metering. This runs the broker, identity webhook, and remote signer.

```bash
cp kafka/.env.example kafka/.env
cp identity-webhook/.env.example identity-webhook/.env
cp remote-signer/.env.example remote-signer/.env
$EDITOR identity-webhook/.env remote-signer/.env
# WEBHOOK_SECRET must match in both files (`.env.example` ships a local dev value).
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

Verify CLI port is not published:

```bash
docker compose port remote-signer 4935
# expected: no output / error (port is not mapped)
docker compose port remote-signer 8081
# expected: 0.0.0.0:8081
```

### 2. Full stack — add metering

After the quick check passes, add the OpenMeter collector and hosted metering configuration. Provision OpenMeter meters/features (see [OpenMeter/Konnect bootstrap](#openmeterkonnect-bootstrap)), then configure the collector:

```bash
cp openmeter-collector/.env.example openmeter-collector/.env
$EDITOR openmeter-collector/.env

docker compose up -d --build
docker compose logs -f
docker compose down
```

## Environment variables

Each service documents its variables in its own `.env.example`:

| Service | Config file | Key variables |
| --- | --- | --- |
| `identity-webhook` | [`identity-webhook/.env.example`](identity-webhook/.env.example) | `WEBHOOK_SECRET`, `IDENTITY_ISSUER`, `DEMO_API_KEY`, `DEMO_CLIENT_ID`, `DEMO_USER_ID`, `API_KEY_PREFIX` (optional, default `sk_`) |
| `kafka` | [`kafka/.env.example`](kafka/.env.example) | `KAFKA_ADVERTISED_ADDR` |
| `remote-signer` | [`remote-signer/.env.example`](remote-signer/.env.example) | `REMOTE_SIGNER_WEBHOOK_URL`, `WEBHOOK_SECRET`, `SIGNER_*`, `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC` |
| `openmeter-collector` | [`openmeter-collector/.env.example`](openmeter-collector/.env.example) | `KAFKA_BROKERS`, `KAFKA_GATEWAY_TOPIC`, `OPENMETER_URL`, `OPENMETER_INGEST_URL`, `OPENMETER_API_KEY`, `OPENMETER_DEFAULT_PLAN_KEY`, `ETH_USD_PRICE` |

Signer state (keystore, `.eth-password`, chain DB) is stored under [`remote-signer/data/`](remote-signer/data/), bind-mounted to `/data` in the container.

```bash
mkdir -p remote-signer/data/keystore
cp /path/to/your/keystore/* remote-signer/data/keystore/
cp /path/to/your/.eth-password remote-signer/data/.eth-password

cp remote-signer/.env.example remote-signer/.env
$EDITOR remote-signer/.env
```

Set `SIGNER_ETH_KEYSTORE_PATH=/data/keystore` (container path) and `SIGNER_ETH_ADDR` to your funded signer address. If `SIGNER_ETH_KEYSTORE_PATH` is unset, the entrypoint uses `/data/keystore` when that directory exists.

To change the host signing port from `8081`, use a Compose override file.

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

Markup rules are defined in the bootstrap CLI catalog. Collector
pipeline config: [`openmeter-collector/collector.yaml`](openmeter-collector/collector.yaml).
`billable_usd_micros` is initially set equal to `network_fee_usd_micros`; markup rules will diverge in a later phase.

### Customer upsert (collector self-heal)

The collector runs a local Go provision sidecar (`openmeter-collector/provision`, Kong `sdk-konnect-go`) that holds the
OpenMeter admin credentials — the identity webhook does **not** need them.

For each `create_signed_ticket` event:

1. Benthos maps the CloudEvent (including `billable_usd_micros`, initially equal to `network_fee_usd_micros`).
2. `POST http://127.0.0.1:8091/ensure` idempotently creates customer + subscription (`OPENMETER_DEFAULT_PLAN_KEY`).
3. Event is ingested to Konnect.
4. On ingest failure (e.g. `no customer found for event subject`), the collector ensures again and retries once.

### Future admin/query boundary (OAuth later)

When an admin/query API is added, introduce a small internal **billing-gateway** service:

- Move ensure-customer and usage-query logic behind that gateway.
- Protect caller-to-gateway with OAuth (client credentials / service-to-service).
- Keep gateway-to-OpenMeter on backend machine credentials (`kpat_…`).

The collector provision sidecar is the thin local equivalent until that gateway exists.

### Identity contract (collector)

The collector expects Kafka `auth_id` as `client_id:external_user_id` (first-colon split).
Konnect customer key matches that compound id (e.g. `demo-client:demo-user`).

Demo API key defaults: `sk_demo_local_key` → `demo-client:demo-user` (configured in `identity-webhook/.env`).

Customer upsert is handled by the collector provision sidecar (see above). The Go bootstrap CLI
still provisions meters/features/plans; per-event customer+subscription ensure runs in the collector.
