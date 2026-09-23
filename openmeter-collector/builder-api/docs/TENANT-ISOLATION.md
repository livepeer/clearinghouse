# Usage isolation on shared OpenMeter

Builder API now exposes only **user-scoped usage reads**:

- `GET /api/v1/users/me/usage` with Bearer signer JWT
- no tenant-admin Basic usage routes

## Boundary model

- One shared Konnect/OpenMeter organization
- End-user identity from signer JWT claims (`app_client_id`, `external_user_id`)
- Usage queries always target exactly one subject:
  `{clientId}:{externalUserId}`

This keeps tenant boundaries claim-driven instead of path-driven.

## Required runtime config

```bash
OPENMETER_URL=https://us.api.konghq.com/v3/openmeter
OPENMETER_API_KEY=kpat_...
REMOTE_SIGNER_WEBHOOK_URL=http://identity-webhook:8090/authorize
WEBHOOK_SECRET=...
```

If usage backend or JWT verification is not configured, usage routes return
`503`.

Optional Kong Gateway / Dev Portal CORS: [DEV-PORTAL.md](DEV-PORTAL.md).
`CORS_ALLOW_KONG_PORTALS` (default true) and `CORS_ALLOWED_ORIGINS`.
