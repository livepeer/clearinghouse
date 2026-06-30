import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { SignJWT, createLocalJWKSet, exportJWK, generateKeyPair } from "jose";
import {
  createApiKeyVerifier,
  createEndUserVerifierFromEnv,
  createOidcVerifier,
} from "./verifiers.mjs";

const ISSUER = "http://identity-webhook:8090";

describe("createApiKeyVerifier", () => {
  const store = new Map([
    ["sk_demo", { userId: "demo-user", clientId: "demo-client", usageSubjectType: "api_key_user" }],
  ]);
  const verifier = createApiKeyVerifier({
    issuer: ISSUER,
    resolveApiKey: async (k) => store.get(k) ?? null,
  });

  it("resolves a known key to a UsageIdentity", async () => {
    const { identity, expiry } = await verifier.verify({ authorization: "Bearer sk_demo" });
    assert.deepEqual(identity, {
      issuer: ISSUER,
      client_id: "demo-client",
      usage_subject: "demo-user",
      usage_subject_type: "api_key_user",
    });
    assert.equal(typeof expiry, "number");
  });

  it("rejects a token without the configured prefix", async () => {
    await assert.rejects(() => verifier.verify({ authorization: "Bearer nope" }), /invalid api key/);
  });

  it("rejects an unknown key", async () => {
    await assert.rejects(() => verifier.verify({ authorization: "Bearer sk_missing" }), /invalid api key/);
  });
});

describe("createOidcVerifier (jose, locally-minted JWT)", () => {
  // Mint a real RSA keypair + local JWKS so OIDC is tested end-to-end, no network.
  async function setup() {
    const { publicKey, privateKey } = await generateKeyPair("RS256");
    const jwk = await exportJWK(publicKey);
    jwk.kid = "test-key";
    jwk.alg = "RS256";
    jwk.use = "sig";
    const jwks = createLocalJWKSet({ keys: [jwk] });
    return { privateKey, jwks };
  }

  async function mint(privateKey, claims, { aud = "clearinghouse", iss = "https://idp.test/" } = {}) {
    return new SignJWT(claims)
      .setProtectedHeader({ alg: "RS256", kid: "test-key" })
      .setIssuer(iss)
      .setAudience(aud)
      .setSubject(claims.sub ?? "user-b")
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(privateKey);
  }

  it("verifies a valid token and maps claims to a UsageIdentity", async () => {
    const { privateKey, jwks } = await setup();
    const token = await mint(privateKey, { sub: "user-b", azp: "app-b", scope: "signer.use" });
    const verifier = createOidcVerifier({
      jwtIssuer: "https://idp.test/",
      jwtAudience: "clearinghouse",
      jwks,
      issuer: ISSUER,
      requiredScopes: ["signer.use"],
    });
    const { identity, expiry } = await verifier.verify({ authorization: `Bearer ${token}` });
    assert.equal(identity.issuer, ISSUER);
    assert.equal(identity.client_id, "app-b");
    assert.equal(identity.usage_subject, "user-b");
    assert.equal(identity.usage_subject_type, "oidc_user");
    assert.equal(typeof expiry, "number");
  });

  it("rejects a wrong audience", async () => {
    const { privateKey, jwks } = await setup();
    const token = await mint(privateKey, { sub: "user-b", azp: "app-b" }, { aud: "other-api" });
    const verifier = createOidcVerifier({
      jwtIssuer: "https://idp.test/",
      jwtAudience: "clearinghouse",
      jwks,
    });
    await assert.rejects(
      () => verifier.verify({ authorization: `Bearer ${token}` }),
      /oidc verification failed/,
    );
  });

  it("rejects a missing required scope", async () => {
    const { privateKey, jwks } = await setup();
    const token = await mint(privateKey, { sub: "user-b", azp: "app-b", scope: "other" });
    const verifier = createOidcVerifier({
      jwtIssuer: "https://idp.test/",
      jwtAudience: "clearinghouse",
      jwks,
      requiredScopes: ["signer.use"],
    });
    await assert.rejects(
      () => verifier.verify({ authorization: `Bearer ${token}` }),
      /missing required scope/,
    );
  });

  it("rejects a non-JWT bearer", async () => {
    const { jwks } = await setup();
    const verifier = createOidcVerifier({
      jwtIssuer: "https://idp.test/",
      jwtAudience: "clearinghouse",
      jwks,
    });
    await assert.rejects(() => verifier.verify({ authorization: "Bearer sk_not_a_jwt" }), /not a JWT/);
  });
});

describe("createEndUserVerifierFromEnv", () => {
  const apiKeyEnv = {
    IDENTITY_ISSUER: ISSUER,
    IDENTITY_AUTH_MODE: "api_key",
    DEMO_API_KEY: "sk_demo",
    DEMO_CLIENT_ID: "demo-client",
    DEMO_USER_ID: "demo-user",
  };

  it("builds an API-key verifier when IDENTITY_AUTH_MODE=api_key", async () => {
    const verifier = createEndUserVerifierFromEnv(apiKeyEnv);
    assert.equal(verifier.kind, "api_key");
    const { identity } = await verifier.verify({ authorization: "Bearer sk_demo" });
    assert.equal(identity.usage_subject, "demo-user");
  });

  it("rejects oidc mode without OIDC_ISSUER", () => {
    assert.throws(
      () => createEndUserVerifierFromEnv({ ...apiKeyEnv, IDENTITY_AUTH_MODE: "oidc" }),
      /oidc mode requires OIDC_ISSUER/,
    );
  });

  it("oidc verifier rejects a JWT missing a required scope", async () => {
    const { privateKey, jwks } = await (async () => {
      const { publicKey, privateKey } = await generateKeyPair("RS256");
      const jwk = await exportJWK(publicKey);
      jwk.kid = "k";
      jwk.alg = "RS256";
      const jwks = createLocalJWKSet({ keys: [jwk] });
      return { privateKey, jwks };
    })();

    const token = await new SignJWT({ azp: "app-b", scope: "other" })
      .setProtectedHeader({ alg: "RS256", kid: "k" })
      .setIssuer("https://idp.test/")
      .setAudience("clearinghouse")
      .setSubject("user-b")
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(privateKey);

    const verifier = createOidcVerifier({
      jwtIssuer: "https://idp.test/",
      jwtAudience: "clearinghouse",
      jwks,
      issuer: ISSUER,
      requiredScopes: ["signer.use"],
    });

    await assert.rejects(
      () => verifier.verify({ authorization: `Bearer ${token}` }),
      /missing required scope/,
    );
  });

  it("oidc mode does not accept sk_ API keys", async () => {
    const verifier = createEndUserVerifierFromEnv({
      IDENTITY_ISSUER: ISSUER,
      IDENTITY_AUTH_MODE: "oidc",
      OIDC_ISSUER: "https://idp.test/",
      OIDC_AUDIENCE: "clearinghouse",
    });

    assert.equal(verifier.kind, "oidc");
    await assert.rejects(
      () => verifier.verify({ authorization: "Bearer sk_demo" }),
      /not a JWT/,
    );
  });
});
