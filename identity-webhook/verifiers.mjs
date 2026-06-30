/**
 * End-user auth verifiers for the identity webhook.
 *
 * A verifier resolves an end-user credential (from the webhook body's
 * Authorization) into a UsageIdentity:
 *   { issuer, client_id, usage_subject, usage_subject_type }
 *
 * - createApiKeyVerifier: resolves `sk_…` keys via a caller-supplied lookup.
 * - createOidcVerifier:   verifies a JWT bearer against an OIDC issuer's JWKS (jose).
 * - createEndUserVerifierFromEnv: picks exactly one verifier via IDENTITY_AUTH_MODE.
 */
import { createRemoteJWKSet, jwtVerify } from "jose";
import { bearerToken, WebhookError } from "./protocol.mjs";
import { loadApiKeyStore } from "./keys.mjs";

export const IDENTITY_AUTH_MODES = ["api_key", "oidc"];

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
        console.warn(`oidc verification failed: ${err.message}`);
        throw new WebhookError("oidc verification failed", {
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

function envTrim(env, name) {
  return env[name]?.trim() || "";
}

function envOptional(env, name, fallback) {
  return envTrim(env, name) || fallback;
}

/**
 * Build the end-user verifier from env. IDENTITY_AUTH_MODE selects exactly one
 * verifier — no fallback between OIDC and API-key paths.
 */
export function createEndUserVerifierFromEnv(env) {
  const issuer = envTrim(env, "IDENTITY_ISSUER");
  if (!issuer) {
    throw new Error("IDENTITY_ISSUER is required");
  }

  const mode = envTrim(env, "IDENTITY_AUTH_MODE");
  if (!IDENTITY_AUTH_MODES.includes(mode)) {
    throw new Error(`IDENTITY_AUTH_MODE is required (${IDENTITY_AUTH_MODES.join(" | ")})`);
  }

  if (mode === "api_key") {
    if (!envTrim(env, "DEMO_API_KEY") && !envTrim(env, "DEMO_API_KEYS")) {
      throw new Error("api_key mode requires DEMO_API_KEY and/or DEMO_API_KEYS");
    }
    const keyStore = loadApiKeyStore(env);
    return createApiKeyVerifier({
      issuer,
      apiKeyPrefix: envOptional(env, "API_KEY_PREFIX", "sk_"),
      defaultClientId: envOptional(env, "DEMO_CLIENT_ID", "demo-client"),
      defaultUsageSubjectType: envOptional(env, "USAGE_SUBJECT_TYPE", "api_key_user"),
      resolveApiKey: async (apiKey) => keyStore.get(apiKey) ?? null,
    });
  }

  if (!envTrim(env, "OIDC_ISSUER")) {
    throw new Error("oidc mode requires OIDC_ISSUER");
  }
  if (!envTrim(env, "OIDC_AUDIENCE")) {
    throw new Error("oidc mode requires OIDC_AUDIENCE");
  }

  return createOidcVerifier({
    jwtIssuer: envTrim(env, "OIDC_ISSUER"),
    jwtAudience: envTrim(env, "OIDC_AUDIENCE"),
    jwksUri: envTrim(env, "OIDC_JWKS_URI") || undefined,
    issuer,
    clientClaim: envOptional(env, "OIDC_CLIENT_CLAIM", "azp"),
    subjectClaim: envOptional(env, "OIDC_SUBJECT_CLAIM", "sub"),
    subjectTypeValue: envOptional(env, "OIDC_SUBJECT_TYPE", "oidc_user"),
    requiredScopes: (envTrim(env, "OIDC_REQUIRED_SCOPES") || "")
      .split(/[\s,]+/)
      .filter(Boolean),
  });
}
