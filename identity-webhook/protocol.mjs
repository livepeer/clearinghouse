/**
 * go-livepeer remote-signer identity webhook wire protocol.
 *
 * Self-contained implementation of the contract go-livepeer's remote signer
 * speaks when it calls the identity webhook (go-livepeer PR #3897 format).
 * Framework-agnostic: operates on Web `Request`/`Response`.
 *
 * Request (POST /authorize), caller authenticated with the shared WEBHOOK_SECRET:
 *   { "headers": { "Authorization": ["Bearer sk_… | eyJ…"] },  // end-user creds
 *     "authorization": "Bearer …",  // legacy fallback
 *     "state": { … }, "gpu": "h100" }
 *
 * Response the signer expects:
 *   success → 200 { status:200, auth_id:"client_id:usage_subject", identity, expiry }
 *   reject  → 200 { status:<480-483|503|4xx>, reason, code? }  (HTTP 200, status in body)
 *   bad caller secret → HTTP 401, bad JSON → HTTP 400.
 *
 * Consumers may attach an optional `config.checkBalance` hook to enforce a live
 * credit/allowance gate at sign time (see ./balance-gate.mjs). It runs after the
 * identity is verified and can reject with status 483 (insufficient_balance) /
 * 503 (billing_unavailable), or shorten the returned `expiry` so go-livepeer
 * re-authorizes (and re-checks the balance) sooner.
 */
import { timingSafeEqual } from "node:crypto";

/** HTTP statuses go-livepeer's signer returns to gateway clients. */
export const REMOTE_SIGNER_HTTP_STATUS = {
  REFRESH_SESSION: 480,
  PRICE_EXCEEDED: 481,
  NO_TICKETS: 482,
  INSUFFICIENT_BALANCE: 483,
  BILLING_UNAVAILABLE: 503,
};

/** Machine-readable reject codes forwarded through the webhook wire protocol. */
export const REMOTE_SIGNER_ERROR_CODE = {
  INSUFFICIENT_BALANCE: "insufficient_balance",
  BILLING_UNAVAILABLE: "billing_unavailable",
};

/** Error carrying an HTTP status + machine-readable code for the reject response. */
export class WebhookError extends Error {
  constructor(message, { status = 403, code } = {}) {
    super(message);
    this.name = "WebhookError";
    this.status = status;
    this.code = code;
  }
}

/** Strip a `Bearer ` prefix (case-insensitive); returns the raw token otherwise. */
export function bearerToken(authorization) {
  const value = (authorization ?? "").trim();
  return value.replace(/^Bearer\s+/i, "").trim();
}

function timingSafeEqualStrings(a, b) {
  const ab = Buffer.from(String(a));
  const bb = Buffer.from(String(b));
  if (ab.length !== bb.length) {
    return false;
  }
  return timingSafeEqual(ab, bb);
}

/** Validate the caller (go-livepeer) against the shared secret, constant-time. */
export function authenticateWebhookCaller(request, secret) {
  const expected = (secret ?? "").trim();
  if (!expected) {
    return false;
  }
  const candidates = [
    bearerToken(request.headers.get("authorization") ?? ""),
    (request.headers.get("x-api-key") ?? "").trim(),
    (request.headers.get("x-webhook-secret") ?? "").trim(),
  ];
  return candidates.some((c) => c && timingSafeEqualStrings(c, expected));
}

function firstHeaderValue(values) {
  if (Array.isArray(values)) {
    return (values[0] ?? "").trim();
  }
  if (typeof values === "string") {
    return values.trim();
  }
  return "";
}

/** Extract the end-user Authorization from the webhook body (case-insensitive). */
export function authorizationFromPayload(payload) {
  const headers = payload?.headers;
  if (headers && typeof headers === "object") {
    const direct = firstHeaderValue(headers.Authorization);
    if (direct) {
      return direct;
    }
    for (const [key, value] of Object.entries(headers)) {
      if (key.toLowerCase() === "authorization") {
        const got = firstHeaderValue(value);
        if (got) {
          return got;
        }
      }
    }
  }
  return (payload?.authorization ?? "").trim();
}

/** auth_id persisted by go-livepeer RemotePaymentState: "client_id:usage_subject". */
export function authIdFromIdentity(identity) {
  return `${identity.client_id}:${identity.usage_subject}`;
}

export function isValidUsageIdentity(identity) {
  return Boolean(
    identity &&
      identity.issuer &&
      identity.client_id &&
      identity.usage_subject &&
      identity.usage_subject_type,
  );
}

function rejectStatusFromError(err) {
  if (err instanceof WebhookError) {
    const status = err.status >= 400 && err.status < 600 ? err.status : 403;
    const reject = { status, reason: err.message };
    if (err.code) {
      reject.code = err.code;
    }
    return reject;
  }
  return {
    status: 403,
    reason: err instanceof Error ? err.message : "authorization rejected",
  };
}

function paymentWebhookJson(httpStatus, body) {
  return new Response(JSON.stringify(body), {
    status: httpStatus,
    headers: { "content-type": "application/json" },
  });
}

/** Handle POST /authorize: caller auth → parse → verify end user → response. */
export async function handleAuthorize(request, config) {
  if (!authenticateWebhookCaller(request, config.webhookSecret)) {
    return paymentWebhookJson(401, {
      status: 401,
      reason: "unauthorized webhook caller",
    });
  }

  let payload;
  try {
    payload = await request.json();
  } catch {
    return paymentWebhookJson(400, { status: 400, reason: "invalid request json" });
  }

  const authorization = authorizationFromPayload(payload);
  try {
    const verified = await config.endUserAuth.verify({ authorization, payload, request });
    if (!isValidUsageIdentity(verified.identity)) {
      throw new WebhookError("verifier returned incomplete identity", { status: 500 });
    }

    // Optional live balance/credit gate, applied after identity is proven and
    // regardless of verifier kind (OIDC, composite, API key). Throwing a
    // WebhookError (e.g. status 483 insufficient_balance) rejects the request;
    // returning `{ expiry }` caps how long go-livepeer may cache this auth
    // before it must call back and re-check the balance.
    let expiry = verified.expiry;
    if (typeof config.checkBalance === "function") {
      const decision = await config.checkBalance({
        identity: verified.identity,
        expiry: verified.expiry,
        raw: verified.raw,
        payload,
        request,
      });
      if (decision && typeof decision.expiry === "number") {
        expiry = Math.min(expiry, decision.expiry);
      }
    }

    return paymentWebhookJson(200, {
      status: 200,
      expiry,
      auth_id: authIdFromIdentity(verified.identity),
      identity: verified.identity,
    });
  } catch (err) {
    const { status, reason, code } = rejectStatusFromError(err);
    const body = { status, reason };
    if (code) {
      body.code = code;
    }
    // Rejects ride back on HTTP 200 with the real status in the body (go-livepeer contract).
    return paymentWebhookJson(200, body);
  }
}

/** Route POST /authorize plus any verifier-supplied admin routes (e.g. JWKS refresh). */
export async function routeWebhookRequest(request, config) {
  const url = new URL(request.url);
  if (request.method === "POST" && url.pathname === "/authorize") {
    return handleAuthorize(request, config);
  }
  for (const route of config.endUserAuth.adminRoutes ?? []) {
    if (request.method === route.method && url.pathname === route.pathname) {
      return route.handler(request);
    }
  }
  return null;
}
