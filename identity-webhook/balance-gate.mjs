/**
 * Live balance/credit gate for the identity webhook.
 *
 * `createBalanceGate` turns a simple per-identity balance lookup into a
 * `checkBalance` hook for `handleAuthorize` (protocol.mjs). It is verifier
 * agnostic: whatever proved the identity (OIDC JWT, composite API key, or a
 * plain API key), the gate reads the caller-supplied balance and rejects with
 * the go-livepeer wire status 483 (`insufficient_balance`) when the customer is
 * out of credit — closing the "still streaming after credit hits zero" gap that
 * a mint-time-only gate leaves open.
 *
 * Balances are USD micros (1 USD = 1_000_000 micros), accepted as bigint,
 * safe integer number, or integer string.
 *
 * Example (published package; in-repo relative `./protocol.mjs` also works):
 *   import { handleAuthorize } from "@pymthouse/clearinghouse-identity-webhook/protocol";
 *   import { createBalanceGate } from "@pymthouse/clearinghouse-identity-webhook/balance-gate";
 *
 *   const checkBalance = createBalanceGate({
 *     getBalanceUsdMicros: async (identity) =>
 *       readLiveCreditBalanceUsdMicros(identity.client_id, identity.usage_subject),
 *     expiryTtl: { seconds: 30 }, // sets webhook expiry = now + 30s
 *   });
 *   return handleAuthorize(request, { webhookSecret, endUserAuth, checkBalance });
 */
import {
  REMOTE_SIGNER_ERROR_CODE,
  REMOTE_SIGNER_HTTP_STATUS,
  WebhookError,
} from "./protocol.mjs";

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

/**
 * Coerce a USD-micros balance (bigint | safe integer number | integer string)
 * to a bigint. Returns null for anything non-integer (including "1.5", "", null).
 * Numbers outside Number.MAX_SAFE_INTEGER must be passed as string or bigint.
 */
export function parseUsdMicros(value) {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number") {
    return Number.isSafeInteger(value) ? BigInt(value) : null;
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!/^-?\d+$/.test(trimmed)) {
      return null;
    }
    try {
      return BigInt(trimmed);
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * Build a `checkBalance` hook from a balance lookup.
 *
 * @param {object} options
 * @param {(identity: import("./protocol.mjs").UsageIdentity, ctx: import("./protocol.mjs").BalanceCheckContext) => any} options.getBalanceUsdMicros
 *   Resolve remaining balance (USD micros) for the identity. May be async.
 *   Return null/undefined to signal "balance unknown" (see failClosed).
 * @param {bigint | number | string} [options.minBalanceUsdMicros=1]
 *   Minimum balance required to authorize (non-negative). Default: 1 micro.
 * @param {{ seconds: number }} [options.expiryTtl]
 *   When set, sets the webhook response `expiry` to now + this many whole
 *   seconds (also capped against the verifier expiry). go-livepeer stores that
 *   as AuthExpiry and skips /authorize until it elapses.
 * @param {boolean} [options.failClosed=true]
 *   On lookup error or unknown balance: true → reject 503 billing_unavailable;
 *   false → allow (fail open).
 * @param {(err: unknown, identity: import("./protocol.mjs").UsageIdentity) => void} [options.onError]
 *   Optional hook to observe lookup errors / unparseable balances.
 * @returns {import("./protocol.mjs").BalanceCheck}
 */
export function createBalanceGate({
  getBalanceUsdMicros,
  minBalanceUsdMicros = 1n,
  expiryTtl,
  failClosed = true,
  onError,
} = {}) {
  if (typeof getBalanceUsdMicros !== "function") {
    throw new TypeError("createBalanceGate: getBalanceUsdMicros is required");
  }
  const minBalance = parseUsdMicros(minBalanceUsdMicros);
  if (minBalance === null) {
    throw new TypeError("createBalanceGate: minBalanceUsdMicros must be an integer");
  }
  if (minBalance < 0n) {
    throw new TypeError("createBalanceGate: minBalanceUsdMicros must not be negative");
  }
  let ttl = null;
  if (expiryTtl != null) {
    if (
      typeof expiryTtl !== "object" ||
      expiryTtl === null ||
      !("seconds" in expiryTtl)
    ) {
      throw new TypeError(
        "createBalanceGate: expiryTtl must be an object with positive integer seconds",
      );
    }
    ttl = Number(expiryTtl.seconds);
    if (!Number.isInteger(ttl) || ttl <= 0) {
      throw new TypeError(
        "createBalanceGate: expiryTtl.seconds must be a positive integer",
      );
    }
  }

  const billingUnavailable = (message) =>
    new WebhookError(message, {
      status: REMOTE_SIGNER_HTTP_STATUS.BILLING_UNAVAILABLE,
      code: REMOTE_SIGNER_ERROR_CODE.BILLING_UNAVAILABLE,
    });

  return async function checkBalance(ctx) {
    let rawBalance;
    try {
      rawBalance = await getBalanceUsdMicros(ctx.identity, ctx);
    } catch (err) {
      onError?.(err, ctx.identity);
      if (failClosed) {
        throw billingUnavailable("billing balance lookup failed");
      }
      return undefined;
    }

    const balance = parseUsdMicros(rawBalance);
    if (balance === null) {
      onError?.(
        new Error(`balance is not an integer micros value: ${String(rawBalance)}`),
        ctx.identity,
      );
      if (failClosed) {
        throw billingUnavailable("billing balance unavailable");
      }
      return undefined;
    }

    if (balance < minBalance) {
      throw new WebhookError("insufficient balance", {
        status: REMOTE_SIGNER_HTTP_STATUS.INSUFFICIENT_BALANCE,
        code: REMOTE_SIGNER_ERROR_CODE.INSUFFICIENT_BALANCE,
      });
    }

    if (ttl !== null) {
      return { expiry: nowSeconds() + ttl };
    }
    return undefined;
  };
}
