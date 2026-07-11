import type { EndUserAuthVerifier } from "./protocol.js";

export const IDENTITY_AUTH_MODES: readonly ["api_key", "oidc"];

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

export function createOidcVerifier(options: {
  jwtIssuer: string;
  jwtAudience: string;
  jwks?: import("jose").KeyLike | Uint8Array;
  jwksUri?: string;
  issuer?: string;
  clientClaim?: string;
  subjectClaim?: string;
  subjectTypeValue?: string;
  requiredScopes?: string[];
}): EndUserAuthVerifier;

export function createEndUserVerifierFromEnv(
  env: NodeJS.ProcessEnv | Record<string, string | undefined>,
): EndUserAuthVerifier;
