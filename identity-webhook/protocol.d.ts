export const REMOTE_SIGNER_HTTP_STATUS: {
  readonly REFRESH_SESSION: 480;
  readonly PRICE_EXCEEDED: 481;
  readonly NO_TICKETS: 482;
  readonly INSUFFICIENT_BALANCE: 483;
  readonly BILLING_UNAVAILABLE: 503;
};

export const REMOTE_SIGNER_ERROR_CODE: {
  readonly INSUFFICIENT_BALANCE: "insufficient_balance";
  readonly BILLING_UNAVAILABLE: "billing_unavailable";
};

export class WebhookError extends Error {
  status: number;
  code?: string;
  constructor(
    message: string,
    options?: { status?: number; code?: string },
  );
}

export function bearerToken(authorization: string | undefined): string;

export function authenticateWebhookCaller(
  request: Request,
  secret: string,
): boolean;

export function authorizationFromPayload(payload: {
  headers?: Record<string, string | string[]>;
  authorization?: string;
}): string;

export type UsageIdentity = {
  issuer: string;
  client_id: string;
  usage_subject: string;
  usage_subject_type: string;
};

export function authIdFromIdentity(identity: UsageIdentity): string;

export function isValidUsageIdentity(
  identity: unknown,
): identity is UsageIdentity;

export type PaymentWebhookResponse = {
  status: number;
  reason?: string;
  code?: string;
  expiry?: number;
  auth_id?: string;
  identity?: UsageIdentity;
};

export type EndUserAuthVerifier = {
  kind: string;
  verify: (ctx: {
    authorization: string;
    payload: unknown;
    request: Request;
  }) => Promise<{
    identity: UsageIdentity;
    expiry: number;
    raw?: unknown;
  }>;
  adminRoutes?: Array<{
    method: string;
    pathname: string;
    handler: (request: Request) => Promise<Response>;
  }>;
};

export type BalanceCheckContext = {
  identity: UsageIdentity;
  expiry: number;
  raw?: unknown;
  payload: unknown;
  request: Request;
};

export type BalanceCheckResult = { expiry?: number } | void;

/**
 * Live balance/credit gate invoked after identity verification. Throw a
 * `WebhookError` (e.g. status 483 `insufficient_balance`) to reject; optionally
 * return `{ expiry }` to cap how long go-livepeer caches this authorization.
 */
export type BalanceCheck = (
  ctx: BalanceCheckContext,
) => Promise<BalanceCheckResult> | BalanceCheckResult;

export type RemoteSignerWebhookConfig = {
  webhookSecret: string;
  endUserAuth: EndUserAuthVerifier;
  checkBalance?: BalanceCheck;
};

export function handleAuthorize(
  request: Request,
  config: RemoteSignerWebhookConfig,
): Promise<Response>;

export function routeWebhookRequest(
  request: Request,
  config: RemoteSignerWebhookConfig,
): Promise<Response | null>;
