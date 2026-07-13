# `@livepeer/clearinghouse-identity-webhook`

API-key and OIDC identity webhook helpers for [go-livepeer](https://github.com/livepeer/go-livepeer) remote signer authorization.

This package exports the wire protocol and end-user verifiers used by the clearinghouse identity webhook. The HTTP service in this directory (`server.mjs`) is not part of the published package.

## Install

```bash
npm install @livepeer/clearinghouse-identity-webhook
```

Requires Node.js 20+.

## Exports

| Subpath | Purpose |
| --- | --- |
| `@livepeer/clearinghouse-identity-webhook/protocol` | go-livepeer `/authorize` webhook wire protocol (`handleAuthorize`, `routeWebhookRequest`, `WebhookError`, identity helpers) |
| `@livepeer/clearinghouse-identity-webhook/verifiers` | End-user credential verifiers (`createApiKeyVerifier`, `createOidcVerifier`, `createEndUserVerifierFromEnv`) |

## Usage

```js
import {
  handleAuthorize,
  routeWebhookRequest,
} from "@livepeer/clearinghouse-identity-webhook/protocol";
import {
  createApiKeyVerifier,
  createOidcVerifier,
  createEndUserVerifierFromEnv,
} from "@livepeer/clearinghouse-identity-webhook/verifiers";

const endUserAuth = createEndUserVerifierFromEnv(process.env);
// or: createApiKeyVerifier({ issuer, resolveApiKey })
// or: createOidcVerifier({ jwtIssuer, jwtAudience, jwksUri })

export default {
  async fetch(request) {
    return routeWebhookRequest(request, {
      webhookSecret: process.env.WEBHOOK_SECRET,
      endUserAuth,
    });
  },
};
```

Successful authorization resolves to `auth_id = "{client_id}:{usage_subject}"` for multi-tenant usage attribution.

## Environment (verifiers)

When using `createEndUserVerifierFromEnv`, set `IDENTITY_AUTH_MODE` to exactly one of `api_key` or `oidc` (no fallback). See the [clearinghouse README](https://github.com/livepeer/clearinghouse#identity-webhook) for the full variable list.

## Development

```bash
npm test
npm pack --dry-run
```

Releases are cut from this subdirectory via semver tags. See [docs/RELEASING.md](./docs/RELEASING.md).

## License

MIT
