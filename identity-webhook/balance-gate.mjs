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
 * integer number, or integer string.
 *
 * Example:
 *   import { handleAuthorize } from "@livepeer/clearinghouse-identity-webhook/protocol";
 *   import { createBalanceGate } from "@livepeer/clearinghouse-identity-webhook/balance-gate";
 *
 *   const checkBalance = createBalanceGate({
 *     getBalanceUsdMicros: async (identity) =>
 *       readLiveCreditBalanceUsdMicros(identity.client_id, identity.usage_subject),
 *     reauthTtlSeconds: 30, // re-check at least every 30s mid-stream
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
 * Coerce a USD-micros balance (bigint | integer number | integer string) to a
 * bigint. Returns null for anything non-integer (including "1.5", "", null).
 */
export function parseUsdMicros(value) {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number") {
    return Number.isInteger(value) ? BigInt(value) : null;
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
 * @param {(identity: import("./protocol.js").UsageIdentity, ctx: import("./protocol.js").BalanceCheckContext) => any} options.getBalanceUsdMicros
 *   Resolve remaining balance (USD micros) for the identity. May be async.
 *   Return null/undefined to signal "balance unknown" (see failClosed).
 * @param {bigint | number | string} [options.minBalanceUsdMicros=1]
 *   Minimum balance required to authorize. Default: 1 micro (any positive credit).
 * @param {number} [options.reauthTtlSeconds]
 *   When set, caps the returned expiry to now + this, forcing go-livepeer to
 *   call back and re-check the balance at least this often.
 * @param {boolean} [options.failClosed=true]
 *   On lookup error or unknown balance: true → reject 503 billing_unavailable;
 *   false → allow (fail open).
 * @param {(err: unknown, identity: import("./protocol.js").UsageIdentity) => void} [options.onError]
 *   Optional hook to observe lookup errors / unparseable balances.
 * @returns {import("./protocol.js").BalanceCheck}
 */
export function createBalanceGate({
  getBalanceUsdMicros,
  minBalanceUsdMicros = 1n,
  reauthTtlSeconds,
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
  let ttl = null;
  if (reauthTtlSeconds != null) {
    ttl = Number(reauthTtlSeconds);
    if (!Number.isFinite(ttl) || ttl <= 0) {
      throw new TypeError("createBalanceGate: reauthTtlSeconds must be a positive number");
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
