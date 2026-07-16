import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { SignJWT, createLocalJWKSet, exportJWK, generateKeyPair } from "jose";
import {
  createApiKeyVerifier,
  createEndUserVerifierFromEnv,
  createOidcVerifier,
  discoverJwksUri,
} from "./verifiers.mjs";

const ISSUER = "http://identity-webhook:8090";

describe("discoverJwksUri", () => {
  it("reads jwks_uri from issuer-relative openid-configuration", async () => {
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    const seen = [];
    const fetchImpl = async (input) => {
      seen.push(String(input));
      return Response.json({
        issuer,
        jwks_uri: jwksUri,
      });
    };
    const uri = await discoverJwksUri(issuer, {
      fetchImpl,
    });
    assert.equal(uri, jwksUri);
    assert.deepEqual(seen, [`${issuer}/.well-known/openid-configuration`]);
  });

  it("rejects discovery without jwks_uri", async () => {
    await assert.rejects(
      () =>
        discoverJwksUri("https://idp.test", {
          fetchImpl: async () => Response.json({ issuer: "https://idp.test" }),
        }),
      /missing jwks_uri/,
    );
  });

  it("rejects discovery when issuer does not match jwtIssuer", async () => {
    await assert.rejects(
      () =>
        discoverJwksUri("https://idp.test/api/v1/oidc", {
          fetchImpl: async () =>
            Response.json({
              issuer: "https://other.example/oidc",
              jwks_uri: "https://other.example/oidc/jwks",
            }),
        }),
      /issuer mismatch/,
    );
  });

  it("accepts discovery issuer that differs only by trailing slash", async () => {
    const uri = await discoverJwksUri("https://idp.test/api/v1/oidc/", {
      fetchImpl: async () =>
        Response.json({
          issuer: "https://idp.test/api/v1/oidc",
          jwks_uri: "https://idp.test/api/v1/oidc/jwks",
        }),
    });
    assert.equal(uri, "https://idp.test/api/v1/oidc/jwks");
  });

  it("trims surrounding whitespace on jwtIssuer and discovery issuer", async () => {
    const uri = await discoverJwksUri("  https://idp.test/api/v1/oidc/  ", {
      fetchImpl: async () =>
        Response.json({
          issuer: " https://idp.test/api/v1/oidc ",
          jwks_uri: "https://idp.test/api/v1/oidc/jwks",
        }),
    });
    assert.equal(uri, "https://idp.test/api/v1/oidc/jwks");
  });

  it("rejects a blank jwtIssuer", async () => {
    await assert.rejects(() => discoverJwksUri("   "), /jwtIssuer is required/);
    await assert.rejects(() => discoverJwksUri(""), /jwtIssuer is required/);
  });
});

describe("createOidcVerifier discovery", () => {
  it("discovers JWKS via openid-configuration then verifies", async () => {
    const { publicKey, privateKey } = await generateKeyPair("RS256");
    const jwk = await exportJWK(publicKey);
    jwk.kid = "disc-key";
    jwk.alg = "RS256";
    jwk.use = "sig";
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    const seen = [];
    const fetchImpl = async (input) => {
      const url = String(input);
      seen.push(url);
      if (url.endsWith("/.well-known/openid-configuration")) {
        return Response.json({ issuer, jwks_uri: jwksUri });
      }
      if (url.startsWith(jwksUri)) {
        return Response.json({ keys: [jwk] });
      }
      return new Response("not found", { status: 404 });
    };

    const token = await new SignJWT({ sub: "user-b", azp: "app-b" })
      .setProtectedHeader({ alg: "RS256", kid: "disc-key" })
      .setIssuer(issuer)
      .setAudience("clearinghouse")
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(privateKey);

    const verifier = createOidcVerifier({
      jwtIssuer: issuer,
      jwtAudience: "clearinghouse",
      fetchImpl,
    });
    const { identity } = await verifier.verify({ authorization: `Bearer ${token}` });
    assert.equal(identity.usage_subject, "user-b");
    assert.ok(seen.some((u) => u.endsWith("/.well-known/openid-configuration")));
    assert.ok(seen.some((u) => u.startsWith(jwksUri)));
    assert.ok(!seen.some((u) => u.includes(".well-known/jwks.json")));
  });

  it("wraps JWKS fetch failures with the JWKS URL", async () => {
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    const fetchImpl = async (input) => {
      const url = String(input);
      if (url.endsWith("/.well-known/openid-configuration")) {
        return Response.json({ issuer, jwks_uri: jwksUri });
      }
      if (url.startsWith(jwksUri)) {
        throw new Error("network down");
      }
      return new Response("not found", { status: 404 });
    };

    const warnings = [];
    const origWarn = console.warn;
    console.warn = (...args) => warnings.push(args.join(" "));
    try {
      const verifier = createOidcVerifier({
        jwtIssuer: issuer,
        jwtAudience: "clearinghouse",
        fetchImpl,
      });
      await assert.rejects(
        () => verifier.verify({ authorization: "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig" }),
        /oidc verification failed/,
      );
    } finally {
      console.warn = origWarn;
    }
    assert.ok(
      warnings.some((w) =>
        w.includes(`JWKS request failed (${jwksUri}): network down`),
      ),
      `expected JWKS URL in warn log, got: ${JSON.stringify(warnings)}`,
    );
  });

  it("wraps non-ok JWKS HTTP responses with the JWKS URL", async () => {
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    const fetchImpl = async (input) => {
      const url = String(input);
      if (url.endsWith("/.well-known/openid-configuration")) {
        return Response.json({ issuer, jwks_uri: jwksUri });
      }
      if (url.startsWith(jwksUri)) {
        return new Response("gone", { status: 503 });
      }
      return new Response("not found", { status: 404 });
    };

    const warnings = [];
    const origWarn = console.warn;
    console.warn = (...args) => warnings.push(args.join(" "));
    try {
      const verifier = createOidcVerifier({
        jwtIssuer: issuer,
        jwtAudience: "clearinghouse",
        fetchImpl,
      });
      await assert.rejects(
        () => verifier.verify({ authorization: "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig" }),
        /oidc verification failed/,
      );
    } finally {
      console.warn = origWarn;
    }
    assert.ok(
      warnings.some((w) => w.includes(`JWKS request failed (${jwksUri}): HTTP 503`)),
      `expected JWKS HTTP status in warn log, got: ${JSON.stringify(warnings)}`,
    );
  });

  it("wraps invalid JWKS documents with the JWKS URL", async () => {
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    const fetchImpl = async (input) => {
      const url = String(input);
      if (url.endsWith("/.well-known/openid-configuration")) {
        return Response.json({ issuer, jwks_uri: jwksUri });
      }
      if (url.startsWith(jwksUri)) {
        return Response.json({ keys: "not-an-array" });
      }
      return new Response("not found", { status: 404 });
    };

    const warnings = [];
    const origWarn = console.warn;
    console.warn = (...args) => warnings.push(args.join(" "));
    try {
      const verifier = createOidcVerifier({
        jwtIssuer: issuer,
        jwtAudience: "clearinghouse",
        fetchImpl,
      });
      await assert.rejects(
        () => verifier.verify({ authorization: "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig" }),
        /oidc verification failed/,
      );
    } finally {
      console.warn = origWarn;
    }
    assert.ok(
      warnings.some((w) => w.includes(`JWKS is invalid (${jwksUri})`)),
      `expected JWKS URL in warn log, got: ${JSON.stringify(warnings)}`,
    );
  });

  it("retries discovery after a transient failure", async () => {
    const { publicKey, privateKey } = await generateKeyPair("RS256");
    const jwk = await exportJWK(publicKey);
    jwk.kid = "retry-key";
    jwk.alg = "RS256";
    jwk.use = "sig";
    const issuer = "https://idp.test/api/v1/oidc";
    const jwksUri = `${issuer}/jwks`;
    let discoveryAttempts = 0;
    const fetchImpl = async (input) => {
      const url = String(input);
      if (url.endsWith("/.well-known/openid-configuration")) {
        discoveryAttempts += 1;
        if (discoveryAttempts === 1) {
          throw new Error("temporary outage");
        }
        return Response.json({ issuer, jwks_uri: jwksUri });
      }
      if (url.startsWith(jwksUri)) {
        return Response.json({ keys: [jwk] });
      }
      return new Response("not found", { status: 404 });
    };

    const token = await new SignJWT({ sub: "user-retry", azp: "app-retry" })
      .setProtectedHeader({ alg: "RS256", kid: "retry-key" })
      .setIssuer(issuer)
      .setAudience("clearinghouse")
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(privateKey);

    const verifier = createOidcVerifier({
      jwtIssuer: issuer,
      jwtAudience: "clearinghouse",
      fetchImpl,
    });

    await assert.rejects(
      () => verifier.verify({ authorization: `Bearer ${token}` }),
      /oidc verification failed/,
    );

    const { identity } = await verifier.verify({ authorization: `Bearer ${token}` });
    assert.equal(identity.usage_subject, "user-retry");
    assert.equal(discoveryAttempts, 2);
  });
});

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

  it("rejects a JWKS JSON object passed as jwks", () => {
    assert.throws(
      () =>
        createOidcVerifier({
          jwtIssuer: "https://idp.test/",
          jwtAudience: "clearinghouse",
          jwks: { keys: [] },
        }),
      /jwks must be a jose key resolver function/,
    );
  });

  it("includes a malformed jwksUri in the verification warning", async () => {
    const warnings = [];
    const origWarn = console.warn;
    console.warn = (...args) => warnings.push(args.join(" "));
    try {
      const verifier = createOidcVerifier({
        jwtIssuer: "https://idp.test/",
        jwtAudience: "clearinghouse",
        jwksUri: "/relative/jwks",
      });
      await assert.rejects(
        () => verifier.verify({ authorization: "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig" }),
        /oidc verification failed/,
      );
    } finally {
      console.warn = origWarn;
    }
    assert.ok(
      warnings.some((w) => w.includes("JWKS URI is not a valid URL (/relative/jwks)")),
      `expected malformed JWKS URI in warn log, got: ${JSON.stringify(warnings)}`,
    );
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
