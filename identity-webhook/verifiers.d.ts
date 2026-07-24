import type { EndUserAuthVerifier } from "./protocol.js";

export const IDENTITY_AUTH_MODES: readonly ["api_key", "oidc"];

export function splitCompositeApiKey(
  token: string,
): { publicClientId: string; apiKey: string } | null;

export function normalizeTokenExchangeBaseUrl(baseUrl: string): string;

export function createApiKeyVerifier(options: {
  issuer: string;
  resolveApiKey: (
    token: string,
  ) =>
    | Promise<{
        userId: string;
        clientId?: string;
        usageSubjectType?: string;
      } | null>
    | {
        userId: string;
        clientId?: string;
        usageSubjectType?: string;
      }
    | null;
  apiKeyPrefix?: string;
  defaultClientId?: string;
  defaultUsageSubjectType?: string;
  expiryTtlSeconds?: number;
}): EndUserAuthVerifier;

export function discoverJwksUri(
  jwtIssuer: string,
  options?: { fetchImpl?: typeof fetch },
): Promise<string>;

export type CompositeExchangeCache = {
  get(key: string): unknown;
  setInflight(key: string, promise: Promise<unknown>): void;
  setResult(key: string, result: unknown, ttlSeconds: number): void;
  clearInflight(key: string, promise: Promise<unknown>): void;
  setResultForInflight(
    key: string,
    promise: Promise<unknown>,
    result: unknown,
    ttlSeconds: number,
  ): void;
  clear(key: string): void;
};

export function createCompositeExchangeCache(): CompositeExchangeCache;

export function createOidcVerifier(options: {
  jwtIssuer: string;
  jwtAudience: string;
  jwks?: import("jose").JWTVerifyGetKey | import("jose").KeyLike | Uint8Array;
  jwksUri?: string;
  issuer?: string;
  clientClaim?: string;
  subjectClaim?: string;
  subjectTypeValue?: string;
  requiredScopes?: string[];
  fetchImpl?: typeof fetch;
  tokenExchangeBaseUrl?: string;
  exchangeM2mClientId?: string;
  exchangeM2mClientSecret?: string;
}): EndUserAuthVerifier;

export function createEndUserVerifierFromEnv(
  env: NodeJS.ProcessEnv | Record<string, string | undefined>,
): EndUserAuthVerifier;
