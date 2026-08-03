# OpenMeter / Konnect metering bootstrap

Idempotent provisioning of the clearinghouse metering catalog (meters, features,
Starter plans) and per-tenant customers, driven by [`kongctl`](https://developer.konghq.com/kongctl/)
against the Konnect Metering & Billing (OpenMeter) API.

Aligned with [pymthouse v0.3.3](https://github.com/pymthouse/pymthouse/releases/tag/v0.3.3)
billing: Starter plans settle on **`network_spend`** (`network_fee_usd_micros`) with
**`credit_then_invoice`** and included cycle allowance via rate-card
**`discounts.usage`** (default $5 / `5000000` micros). Prepaid credits are
manual/onramp top-ups only — bootstrap does **not** auto-grant them.

`kongctl` has no native meter resource yet ([Kong/kongctl#1334](https://github.com/Kong/kongctl/issues/1334)),
so these scripts use `kongctl api` — its authenticated passthrough to the Konnect REST API —
to drive `/v3/openmeter/*`. The catalog is data, defined in [`catalog.json`](catalog.json);
the scripts are thin and idempotent.

## Prerequisites

- `kongctl` and `jq` on `PATH` (the PowerShell script needs only `kongctl`).
- A Konnect Personal Access Token (`kpat_…`) with Metering & Billing access.

## Configuration (env)

Both scripts auto-load the repo-root [`.env`](../../.env) when present (override with
`ENV_FILE`). Put your Konnect PAT there as `OPENMETER_API_KEY` — no need to `source` the
file first (`source .env` does **not** export vars to child processes).

| Variable | Purpose | Default |
| --- | --- | --- |
| `KONGCTL_DEFAULT_KONNECT_PAT` | Konnect PAT (preferred). Falls back to `OPENMETER_API_KEY`. | from `.env` |
| `OPENMETER_API_KEY` | Same PAT as the collector service. | from `.env` |
| `OPENMETER_URL` | Metering API base. **Must match your Konnect org region** (US vs EU). | `https://us.api.konghq.com/v3/openmeter` |
| `OPENMETER_OWNER_STARTER_PLAN_KEY` | Plan key for `owner … --subscribe`. | `clearinghouse_owner_starter` (catalog) |
| `OPENMETER_DEMO_STARTER_PLAN_KEY` | Plan key for M2M `customer … --subscribe`. | `clearinghouse_demo_starter` (catalog) |
| `OPENMETER_DEFAULT_STARTER_INCLUDED_USD_MICROS` | Override rate-card `discounts.usage` when creating plans. | catalog value (`5000000`) |

If `OPENMETER_URL` is unset, the scripts derive it from `OPENMETER_INGEST_URL` (strip `/events`).

One-time setup:

```bash
cp .env.example .env   # at the repo root
# edit OPENMETER_API_KEY=kpat_…
# EU orgs: OPENMETER_URL=https://eu.api.konghq.com/v3/openmeter
#          OPENMETER_INGEST_URL=https://eu.api.konghq.com/v3/openmeter/events
```

## Usage

```bash
cd openmeter-collector/provision

# Catalog only — ensure meters, features, and Starter plans.
./bootstrap.sh catalog

# Provision one M2M / managed-user customer (key = client_id:external_user_id).
./bootstrap.sh customer demo-client demo-user "Demo User"

# Provision a shared app-owner customer (key = bare {users.id}).
./bootstrap.sh owner 2e51154b-d296-4015-990c-02d5f16ecf1e "App Owner"

# Catalog + customer; --subscribe attaches the role-appropriate Starter plan.
./bootstrap.sh all demo-client demo-user "Demo User" --subscribe
./bootstrap.sh owner 2e51154b-d296-4015-990c-02d5f16ecf1e "App Owner" --subscribe
```

Windows (PowerShell):

```powershell
.\bootstrap.ps1 catalog
.\bootstrap.ps1 customer demo-client demo-user "Demo User"
.\bootstrap.ps1 owner 2e51154b-d296-4015-990c-02d5f16ecf1e "App Owner"
.\bootstrap.ps1 all demo-client demo-user "Demo User" -Subscribe
```

Both scripts are safe to re-run: existing meters are left untouched; features missing
a meter link are recreated; plans are created and published when no active version exists.

## What it provisions

From [`catalog.json`](catalog.json):

| Kind | Key | Notes |
| --- | --- | --- |
| Meter | `network_fee_usd_micros` | SUM settlement meter (exact fractional at ingest) |
| Meter | `billable_usd_micros` | Phase-2 markup meter (not on Starter plans) |
| Meter | `signed_ticket_count` | COUNT |
| Meter | `fee_wei` | Analytics (+ `manifest_id`) |
| Meter | `billable_secs` | Analytics (+ `manifest_id`) |
| Meter | `network_fee_usd_micros_by_manifest` | Session/usage UI analytics |
| Feature | `network_spend` | linked to `network_fee_usd_micros` (Starter settlement) |
| Feature | `billable_spend` | linked to `billable_usd_micros` (phase-2; unused by Starter) |
| Plan | `clearinghouse_owner_starter` | Owner Starter: `network_spend` + `discounts.usage` + `credit_then_invoice` |
| Plan | `clearinghouse_demo_starter` | Demo M2M Starter (same rate-card shape) |

### Allowance model

```text
Usage (network_fee_usd_micros this calendar month)
  → burns plan discounts.usage first   # included / cycle $
  → then prepaid credits               # manual top-ups only
Settlement mode: credit_then_invoice
spendable ≈ remaining discounts.usage + prepaid credits
```

Starter included $ is **`discounts.usage`**, not an auto credit grant.

## Identity contract (important)

OpenMeter attributes usage by exact CloudEvent `subject` match, and **forbids changing a
customer's `subject_keys` once it has an active subscription** — so the key must be
correct from creation. Two customer shapes:

| Caller | Wire `auth_id` | CloudEvent `subject` / customer key | `--subscribe` plan |
| --- | --- | --- | --- |
| **M2M / managed user** | `client_id:external_user_id` | compound `client_id:external_user_id` | `clearinghouse_demo_starter` |
| **App owner** | `client_id:owner:{users.id}` | bare `{users.id}` (shared wallet) | `clearinghouse_owner_starter` |

The webhook stamps `owner:` as a **transport marker**; the collector strips it before
egress. Break usage down per-tenant/user with the `client_id` / `external_user_id` meter
dimensions, not by changing the subject. The scripts never mutate `subject_keys` on
existing customers; they warn if an existing customer is missing the expected key.

Identity mapping is enforced by the Bloblang in [`../collector.yaml`](../collector.yaml)
and covered by Benthos unit tests in [`../collector_benthos_test.yaml`](../collector_benthos_test.yaml).

## Limitations

- Customer lookup lists customers and exact-matches the key locally (the API `key`
  filter is a partial match). For very large customer bases, add pagination.
- Features are immutable except for `unit_cost`; if an existing feature lacks a meter
  link (e.g. created with an older bootstrap), the script deletes and recreates it.
- Subscriptions are best-effort with `--subscribe`; plan pricing / discount changes on
  an already-published plan require a new plan version in Konnect (out of scope).
- No free/sandbox billing-profile attach (pymthouse uses `pymthouse-free`); Konnect orgs
  that require a Stripe profile may need that configured once in the UI.
- No prepaid credit grant helper yet — use the Konnect UI/API for MoonPay-style top-ups.

## Konnect first-time setup

Walkthrough for provisioning against a fresh Konnect org:

1. **Create org** — from the org picker, click **+ Create** to provision a clean Metering & Billing workspace.
2. **Name the org** — e.g. *Clearinghouse Example Org*.
3. **Create a Personal Access Token** — Profile menu → **Personal access tokens** → **Generate**. Copy the `kpat_…` token immediately (shown once).
4. **Configure `.env`** — copy `.env.example` to `.env` at the repo root, set `OPENMETER_API_KEY`, and set `OPENMETER_URL` / `OPENMETER_INGEST_URL` to your org's region (`us` or `eu`).
5. **Run bootstrap** — `cd openmeter-collector/provision && ./bootstrap.sh catalog`. Re-run to confirm idempotency (`exists` / `active` lines, no errors).
