# Auth0 client scaffolding (CLI-login bash)

A thin, idempotent bash scaffolder for the clearinghouse Auth0 objects — the resource
server (API), the **public/M2M client pair**, and their client grants — driven entirely by
your **`auth0 login` session**. It is the Auth0 analog of the OpenMeter
[`provision/bootstrap.sh`](../../openmeter-collector/provision): the data lives in
[`apps.json`](apps.json); the script is thin and re-runnable.

Unlike the Go [`auth0ctl`](../README.md) tool, this needs **no Management API client
id/secret** — every call rides your CLI session via `auth0 api` (the authenticated
Management API v2 passthrough), the same way the OpenMeter script uses `kongctl api`.

## Prerequisites

- The [Auth0 CLI](https://github.com/auth0/auth0-cli) and `jq` on `PATH`.
- An authenticated session against the target tenant:

  ```bash
  auth0 login                 # interactive, one-time
  auth0 tenants use <tenant>  # if you have more than one
  ```

## Usage

```bash
cd auth0-provisioner/provision
./bootstrap.sh
```

The script is safe to re-run: it matches the API by `identifier` and clients by `name`
(or optional `public.client_id` / `m2m.client_id` in `apps.json`), reusing existing
ids and updating grant scopes in place — never duplicating. If provisioning an app
fails partway through, only objects created during that run are deleted (rollback);
pre-existing clients and grants are left intact. It writes the resulting ids and M2M
secret to `.env.livepeer` (gitignored).

## What it provisions

From [`apps.json`](apps.json):

| Kind | Identity | Notes |
| --- | --- | --- |
| Resource server | `livepeer-clearinghouse` | API; `identifier` = audience; RS256; `allow_offline_access`; full scope set. |
| Tenant settings | — | `default_audience` + `device_flow` (RFC 8628). Best-effort; skipped with a warning if the session lacks `update:tenant_settings`. |
| Public client | `<App> Public` | `native`, `token_endpoint_auth_method: none`, grants `device_code` + `refresh_token`. |
| M2M client | `<App> M2M` | `non_interactive`, `client_secret_post`, grant `client_credentials`. |
| Management M2M | `Clearinghouse Builder Management` | Auth0 Management API (`create:users`, `read:users`, `update:users`) for Builder API user provisioning. |
| Client grants | per client | Public + M2M each granted their configured scopes against the audience. |

## How it maps to the `auth0` CLI

Each step is a Management API v2 call through the CLI passthrough:

```bash
auth0 api get  "resource-servers?per_page=100"
auth0 api post "resource-servers" --data '{ "identifier": "...", "scopes": [ ... ] }'
auth0 api get  "clients?per_page=100&include_fields=true&fields=client_id,name"
auth0 api post "clients" --data '{ "name": "Demo App Public", "app_type": "native", ... }'
auth0 api get  "client-grants?client_id=...&audience=..."
auth0 api post "client-grants" --data '{ "client_id": "...", "audience": "...", "scope": [ ... ] }'
```

## Verify the device flow (RFC 8628)

### curl

```bash
set -a; source .env.livepeer; set +a
PUB=$DEMO_APP_AUTH0_PUBLIC_CLIENT_ID
curl -s -X POST "${AUTH0_ISSUER}oauth/device/code" \
  -d "client_id=$PUB" -d "audience=$DEMO_APP_AUTH0_AUDIENCE" \
  -d "scope=openid sign:job offline_access"
# open verification_uri_complete, approve, then poll /oauth/token with the device_code grant.
```

### Python (livepeer-gateway)

From the [livepeer-gateway](https://github.com/livepeer/livepeer-gateway) repo (device code → cached bearer → optional `write_frames`):

```bash
uv run examples/device_login.py \
  --issuer https://pymthouse.us.auth0.com \
  --client-id "$DEMO_APP_AUTH0_PUBLIC_CLIENT_ID" \
  --audience "$DEMO_APP_AUTH0_AUDIENCE" \
  --run-frames --signer http://localhost:8081
```

Requires the clearinghouse stack (`identity-webhook`, `remote-signer`, `openmeter-collector`). Pass `--billing-url` and `--client-id` (public Auth0 client) so device login exchanges the Auth0 user token via RFC 8693 `POST …/api/v1/apps/{clientId}/oidc/token` — that upserts the OpenMeter customer (`{clientId}:{sub}`) and returns a minted signer JWT with `signer_url` / `discovery_url`.

## Limitations

- Client lookup pages up to 100 clients and matches by name (the clients endpoint has no
  name filter); for very large tenants, extend the pagination loop.
- Scaffolds and updates only — it never deletes clients/grants removed from `apps.json`.
- Relies on the CLI session's permissions; reading the M2M secret needs the session to
  hold `read:client_keys` (the default interactive `auth0 login` does).

## Builder API follow-up

The Go **Builder API** in `openmeter-collector` provisions Auth0 **end-users**. Re-run `./bootstrap.sh` to
ensure the **Clearinghouse Builder Management** M2M client is created and
`AUTH0_MGMT_CLIENT_ID` / `AUTH0_MGMT_CLIENT_SECRET` are written to `.env.livepeer`
(alongside the Demo App M2M used for signer-token mint).

You still need a **Credentials Exchange Action** that copies `external_user_id` and `client_id`
into minted access tokens:

```bash
./bootstrap-credentials-exchange-action.sh   # idempotent; requires auth0 login
```

See [openmeter-collector/builder-api/README.md](../../openmeter-collector/builder-api/README.md) for claim details and [identity-webhook/.env.example](../../identity-webhook/.env.example) for OIDC verifier env.
