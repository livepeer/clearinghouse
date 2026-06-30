import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { loadApiKeyStore } from "./keys.mjs";

describe("loadApiKeyStore", () => {
  it("loads a primary demo API key", () => {
    const store = loadApiKeyStore({
      DEMO_API_KEY: "sk_demo_local_key",
      DEMO_CLIENT_ID: "demo-client",
      DEMO_USER_ID: "demo-user",
      USAGE_SUBJECT_TYPE: "api_key_user",
    });

    assert.equal(store.size, 1);
    assert.deepEqual(store.get("sk_demo_local_key"), {
      clientId: "demo-client",
      userId: "demo-user",
      usageSubjectType: "api_key_user",
    });
  });

  it("loads extra keys from DEMO_API_KEYS with snake_case fields", () => {
    const store = loadApiKeyStore({
      DEMO_API_KEYS: JSON.stringify({
        sk_other: { client_id: "app-b", user_id: "user-b" },
      }),
    });

    assert.equal(store.size, 1);
    assert.deepEqual(store.get("sk_other"), {
      clientId: "app-b",
      userId: "user-b",
      usageSubjectType: "api_key_user",
    });
  });

  it("rejects invalid DEMO_API_KEYS JSON", () => {
    assert.throws(
      () => loadApiKeyStore({ DEMO_API_KEYS: "not-json" }),
      /DEMO_API_KEYS must be valid JSON/,
    );
  });

  it("requires at least one configured key", () => {
    assert.throws(
      () => loadApiKeyStore({}),
      /DEMO_API_KEY or DEMO_API_KEYS is required/,
    );
  });

  it("skips entries missing userId", () => {
    assert.throws(
      () =>
        loadApiKeyStore({
          DEMO_API_KEYS: JSON.stringify({
            sk_invalid: { clientId: "app-b" },
          }),
        }),
      /DEMO_API_KEY or DEMO_API_KEYS is required/,
    );
  });
});
