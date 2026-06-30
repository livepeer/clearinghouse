# OpenMeter / Konnect metering bootstrap

Idempotent provisioning of the clearinghouse metering catalog (meters, features,
plan) and per-tenant customers, driven by [`kongctl`](https://developer.konghq.com/kongctl/)
against the Konnect Metering & Billing (OpenMeter) API.

`kongctl` has no native meter resource yet ([Kong/kongctl#1334](https://github.com/Kong/kongctl/issues/1334)),
so these scripts use `kongctl api` — its authenticated passthrough to the Konnect REST API —
to drive `/v3/openmeter/*`. The catalog is data, defined in [`catalog.json`](catalog.json);
the scripts are thin and idempotent.

## Prerequisites

- `kongctl` and `jq` on `PATH` (the PowerShell script needs only `kongctl`).
- A Konnect Personal Access Token (`kpat_…`) with Metering & Billing access.

## Configuration (env)

Both scripts auto-load [`openmeter-collector/.env`](../.env) when present (override with
`ENV_FILE`). Put your Konnect PAT there as `OPENMETER_API_KEY` — no need to `source` the
file first (`source .env` does **not** export vars to child processes).

| Variable | Purpose | Default |
| --- | --- | --- |
| `KONGCTL_DEFAULT_KONNECT_PAT` | Konnect PAT (preferred). Falls back to `OPENMETER_API_KEY`. | from `.env` |
| `OPENMETER_API_KEY` | Same PAT as the collector service. | from `.env` |
| `OPENMETER_URL` | Metering API base. | `https://us.api.konghq.com/v3/openmeter` |

One-time setup:

```bash
cp openmeter-collector/.env.example openmeter-collector/.env
# edit OPENMETER_API_KEY=kpat_…
```

## Usage

```bash
cd openmeter-collector/provision

# Catalog only — ensure meters, features, and the active plan.
./bootstrap.sh catalog

# Provision one tenant customer (key = client_id:external_user_id).
./bootstrap.sh customer demo-client demo-user "Demo User"

# Catalog + customer in one run; --subscribe also ensures a plan subscription.
./bootstrap.sh all demo-client demo-user "Demo User" --subscribe
```

Windows (PowerShell):

```powershell
.\bootstrap.ps1 catalog
.\bootstrap.ps1 customer demo-client demo-user "Demo User"
.\bootstrap.ps1 all demo-client demo-user "Demo User" -Subscribe
```

Both scripts are safe to re-run: existing meters are left untouched; features missing
a meter link are recreated; plans are created and published when no active version exists.

## What it provisions

From [`catalog.json`](catalog.json):

| Kind | Key | Notes |
| --- | --- | --- |
| Meter | `network_fee_usd_micros` | SUM of `$.network_fee_usd_micros` |
| Meter | `billable_usd_micros` | SUM of `$.billable_usd_micros` (interim = network fee until phase-2 markup) |
| Meter | `signed_ticket_count` | COUNT |
| Feature | `network_spend` | linked to `network_fee_usd_micros` meter |
| Feature | `billable_spend` | linked to `billable_usd_micros` meter |
| Plan | `clearinghouse_default_ppu` | usage-based rate card on `billable_spend` at $0.000001/unit (1 USD micro); created as draft then published |

## Identity contract (important)

The CloudEvent **`subject` is the compound `client_id:usage_subject`** (e.g.
`demo-client:demo-user`), which is also the customer key and its single `subject_key`.
OpenMeter attributes usage by exact subject match, and **forbids changing a customer's
`subject_keys` once it has an active subscription** — so the subject must be compound and
correct from creation. Break usage down per-tenant/user with the `client_id` / `usage_subject`
meter dimensions, not by changing the subject. The scripts therefore never mutate
`subject_keys` on existing customers; they warn if an existing customer is missing the
expected compound key.

## Limitations

- Customer lookup lists customers and exact-matches the key locally (the API `key`
  filter is a partial match). For very large customer bases, add pagination.
- Features are immutable except for `unit_cost`; if an existing feature lacks a meter
  link (e.g. created with an older bootstrap), the script deletes and recreates it.
- Subscriptions are best-effort with `--subscribe`; plan pricing changes require a new
  plan version in Konnect (out of scope for this script).
