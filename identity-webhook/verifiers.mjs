/**
 * End-user auth verifiers for the identity webhook.
 *
 * A verifier resolves an end-user credential (from the webhook body's
 * Authorization) into a UsageIdentity:
 *   { issuer, client_id, usage_subject, usage_subject_type }
 *
 * - createApiKeyVerifier: resolves `sk_…` keys via a caller-supplied lookup.
 * - createOidcVerifier:   verifies a JWT bearer against an OIDC issuer's JWKS (jose).
 * - createFirstMatchVerifier: tries verifiers in order (OIDC then API key, etc.).
 */
import { createRemoteJWKSet, jwtVerify } from "jose";
import { bearerToken, WebhookError } from "./protocol.mjs";

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

/**
 * API-key verifier. `resolveApiKey(token)` returns
 * { userId, clientId?, usageSubjectType? } or null.
 */
export function createApiKeyVerifier({
  issuer,
  resolveApiKey,
  apiKeyPrefix = "sk_",
  defaultClientId = "demo-client",
  defaultUsageSubjectType = "api_key_user",
  expiryTtlSeconds = 60,
}) {
  if (!issuer) {
    throw new Error("createApiKeyVerifier: issuer is required");
  }
  if (typeof resolveApiKey !== "function") {
    throw new Error("createApiKeyVerifier: resolveApiKey is required");
  }
  return {
    kind: "api_key",
    verify: async ({ authorization }) => {
      const token = bearerToken(authorization);
      if (apiKeyPrefix && !token.startsWith(apiKeyPrefix)) {
        throw new WebhookError("invalid api key", { status: 401, code: "invalid_api_key" });
      }
      const resolved = await resolveApiKey(token);
      if (!resolved?.userId) {
        throw new WebhookError("invalid api key", { status: 401, code: "invalid_api_key" });
      }
      const identity = {
        issuer,
        client_id: resolved.clientId ?? defaultClientId,
        usage_subject: resolved.userId,
        usage_subject_type: resolved.usageSubjectType ?? defaultUsageSubjectType,
      };
      return { identity, expiry: nowSeconds() + expiryTtlSeconds, raw: resolved };
    },
  };
}

/**
 * OIDC/JWT verifier (bring-your-own OAuth). Validates a `Bearer <jwt>` against
 * `jwtIssuer`/`jwtAudience` using the issuer's JWKS (auto-cached & refreshed by
 * jose's createRemoteJWKSet). `jwks` may be injected for testing.
 */
export function createOidcVerifier({
  jwtIssuer,
  jwtAudience,
  jwks,
  jwksUri,
  issuer,
  clientClaim = "azp",
  subjectClaim = "sub",
  subjectTypeValue = "oidc_user",
  requiredScopes = [],
}) {
  if (!jwtIssuer) {
    throw new Error("createOidcVerifier: jwtIssuer is required");
  }
  if (!jwtAudience) {
    throw new Error("createOidcVerifier: jwtAudience is required");
  }
  const keyset =
    jwks ??
    createRemoteJWKSet(
      new URL(jwksUri ?? `${jwtIssuer.replace(/\/$/, "")}/.well-known/jwks.json`),
    );
  const identityIssuer = issuer ?? jwtIssuer;

  return {
    kind: "oidc",
    verify: async ({ authorization }) => {
      const token = bearerToken(authorization);
      if (!token || token.split(".").length !== 3) {
        throw new WebhookError("not a JWT", { status: 401, code: "invalid_token" });
      }

      let payload;
      try {
        ({ payload } = await jwtVerify(token, keyset, {
          issuer: jwtIssuer,
          audience: jwtAudience,
        }));
      } catch (err) {
        throw new WebhookError(`oidc verification failed: ${err.message}`, {
          status: 401,
          code: "invalid_token",
        });
      }

      if (requiredScopes.length) {
        const granted = new Set(
          String(payload.scope ?? payload.scp ?? "")
            .split(/[\s,]+/)
            .filter(Boolean),
        );
        const missing = requiredScopes.filter((s) => !granted.has(s));
        if (missing.length) {
          throw new WebhookError(`missing required scope(s): ${missing.join(", ")}`, {
            status: 403,
            code: "insufficient_scope",
          });
        }
      }

      const usageSubject = payload[subjectClaim];
      if (!usageSubject) {
        throw new WebhookError(`token missing ${subjectClaim} claim`, {
          status: 401,
          code: "invalid_token",
        });
      }

      const identity = {
        issuer: identityIssuer,
        client_id: String(payload[clientClaim] ?? jwtAudience),
        usage_subject: String(usageSubject),
        usage_subject_type: subjectTypeValue,
      };
      const expiry = typeof payload.exp === "number" ? payload.exp : nowSeconds() + 60;
      return { identity, expiry, raw: payload };
    },
  };
}

/** Try each verifier in order; return the first match, else throw the last error. */
export function createFirstMatchVerifier(verifiers) {
  const list = verifiers.filter(Boolean);
  if (!list.length) {
    throw new Error("createFirstMatchVerifier: at least one verifier required");
  }
  const adminRoutes = list.flatMap((v) => v.adminRoutes ?? []);
  return {
    kind: "composite",
    adminRoutes: adminRoutes.length ? adminRoutes : undefined,
    verify: async (context) => {
      let lastErr;
      for (const verifier of list) {
        try {
          return await verifier.verify(context);
        } catch (err) {
          lastErr = err;
        }
      }
      throw (
        lastErr ?? new WebhookError("no verifier matched", { status: 401, code: "invalid_credentials" })
      );
    },
  };
}
