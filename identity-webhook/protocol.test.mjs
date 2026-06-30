import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  authIdFromIdentity,
  authenticateWebhookCaller,
  authorizationFromPayload,
  bearerToken,
  handleAuthorize,
  isValidUsageIdentity,
  routeWebhookRequest,
  WebhookError,
} from "./protocol.mjs";

const SECRET = "test-webhook-secret";

function authorizeRequest({ caller = SECRET, body = {} } = {}) {
  return new Request("http://webhook.test/authorize", {
    method: "POST",
    headers: {
      authorization: `Bearer ${caller}`,
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

// A fixed verifier that maps any token to a known identity (or rejects).
const fakeVerifier = {
  kind: "custom",
  verify: async ({ authorization }) => {
    const token = bearerToken(authorization);
    if (token !== "good-token") {
      throw new WebhookError("nope", { status: 483, code: "insufficient_balance" });
    }
    return {
      identity: {
        issuer: "http://webhook.test",
        client_id: "tenant-a",
        usage_subject: "user-1",
        usage_subject_type: "api_key_user",
      },
      expiry: 1234567890,
    };
  },
};

const config = { webhookSecret: SECRET, endUserAuth: fakeVerifier };

describe("bearerToken", () => {
  it("strips a case-insensitive Bearer prefix", () => {
    assert.equal(bearerToken("Bearer sk_abc"), "sk_abc");
    assert.equal(bearerToken("bearer sk_abc"), "sk_abc");
    assert.equal(bearerToken("sk_abc"), "sk_abc");
    assert.equal(bearerToken(undefined), "");
  });
});

describe("authenticateWebhookCaller", () => {
  it("accepts the matching bearer secret", () => {
    const req = new Request("http://x/authorize", {
      headers: { authorization: `Bearer ${SECRET}` },
    });
    assert.equal(authenticateWebhookCaller(req, SECRET), true);
  });

  it("accepts x-api-key and x-webhook-secret", () => {
    const a = new Request("http://x/authorize", { headers: { "x-api-key": SECRET } });
    const b = new Request("http://x/authorize", { headers: { "x-webhook-secret": SECRET } });
    assert.equal(authenticateWebhookCaller(a, SECRET), true);
    assert.equal(authenticateWebhookCaller(b, SECRET), true);
  });

  it("rejects a wrong or empty secret", () => {
    const req = new Request("http://x/authorize", { headers: { authorization: "Bearer nope" } });
    assert.equal(authenticateWebhookCaller(req, SECRET), false);
    assert.equal(authenticateWebhookCaller(req, ""), false);
  });
});

describe("authorizationFromPayload", () => {
  it("reads the end-user Authorization from the body headers map", () => {
    assert.equal(
      authorizationFromPayload({ headers: { Authorization: ["Bearer sk_demo"] } }),
      "Bearer sk_demo",
    );
  });

  it("is case-insensitive on the header name", () => {
    assert.equal(
      authorizationFromPayload({ headers: { authorization: ["Bearer sk_x"] } }),
      "Bearer sk_x",
    );
  });

  it("falls back to the legacy authorization field", () => {
    assert.equal(authorizationFromPayload({ authorization: "Bearer sk_legacy" }), "Bearer sk_legacy");
  });

  it("returns empty string when absent", () => {
    assert.equal(authorizationFromPayload({}), "");
  });
});

describe("authIdFromIdentity / isValidUsageIdentity", () => {
  it("joins client_id and usage_subject with a colon", () => {
    assert.equal(
      authIdFromIdentity({ client_id: "tenant-a", usage_subject: "user-1" }),
      "tenant-a:user-1",
    );
  });

  it("validates the full identity shape", () => {
    assert.equal(
      isValidUsageIdentity({
        issuer: "i",
        client_id: "c",
        usage_subject: "u",
        usage_subject_type: "t",
      }),
      true,
    );
    assert.equal(isValidUsageIdentity({ client_id: "c", usage_subject: "u" }), false);
  });
});

describe("handleAuthorize", () => {
  it("401s an unauthorized caller", async () => {
    const res = await handleAuthorize(authorizeRequest({ caller: "wrong" }), config);
    assert.equal(res.status, 401);
    assert.deepEqual(await res.json(), { status: 401, reason: "unauthorized webhook caller" });
  });

  it("400s on invalid JSON", async () => {
    const req = new Request("http://webhook.test/authorize", {
      method: "POST",
      headers: { authorization: `Bearer ${SECRET}`, "content-type": "application/json" },
      body: "{not json",
    });
    const res = await handleAuthorize(req, config);
    assert.equal(res.status, 400);
    assert.equal((await res.json()).status, 400);
  });

  it("200s with auth_id on a verified end user", async () => {
    const req = authorizeRequest({ body: { headers: { Authorization: ["Bearer good-token"] } } });
    const res = await handleAuthorize(req, config);
    assert.equal(res.status, 200);
    const out = await res.json();
    assert.equal(out.status, 200);
    assert.equal(out.auth_id, "tenant-a:user-1");
    assert.equal(out.expiry, 1234567890);
    assert.equal(out.identity.usage_subject_type, "api_key_user");
  });

  it("returns HTTP 200 with the reject status in the body on a verifier rejection", async () => {
    const req = authorizeRequest({ body: { headers: { Authorization: ["Bearer bad"] } } });
    const res = await handleAuthorize(req, config);
    assert.equal(res.status, 200); // HTTP 200 per go-livepeer contract
    assert.deepEqual(await res.json(), {
      status: 483,
      reason: "nope",
      code: "insufficient_balance",
    });
  });
});

describe("routeWebhookRequest", () => {
  it("routes POST /authorize", async () => {
    const res = await routeWebhookRequest(
      authorizeRequest({ body: { headers: { Authorization: ["Bearer good-token"] } } }),
      config,
    );
    assert.equal(res.status, 200);
  });

  it("returns null for unknown routes", async () => {
    const res = await routeWebhookRequest(new Request("http://x/nope"), config);
    assert.equal(res, null);
  });
});
