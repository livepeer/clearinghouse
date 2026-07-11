import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { createBalanceGate, parseUsdMicros } from "./balance-gate.mjs";
import { WebhookError } from "./protocol.mjs";

const identity = {
  issuer: "http://webhook.test",
  client_id: "tenant-a",
  usage_subject: "user-1",
  usage_subject_type: "external_user_id",
};

function ctx(overrides = {}) {
  return { identity, expiry: 2000000000, payload: {}, request: new Request("http://x/authorize"), ...overrides };
}

describe("parseUsdMicros", () => {
  it("accepts bigint, integer number, and integer string", () => {
    assert.equal(parseUsdMicros(5n), 5n);
    assert.equal(parseUsdMicros(5), 5n);
    assert.equal(parseUsdMicros("5"), 5n);
    assert.equal(parseUsdMicros("  42 "), 42n);
    assert.equal(parseUsdMicros("0"), 0n);
    assert.equal(parseUsdMicros("-3"), -3n);
  });

  it("rejects non-integer / junk values as null", () => {
    assert.equal(parseUsdMicros(null), null);
    assert.equal(parseUsdMicros(undefined), null);
    assert.equal(parseUsdMicros(""), null);
    assert.equal(parseUsdMicros("1.5"), null);
    assert.equal(parseUsdMicros("abc"), null);
    assert.equal(parseUsdMicros(1.5), null);
  });
});

describe("createBalanceGate", () => {
  it("throws on missing getBalanceUsdMicros", () => {
    assert.throws(() => createBalanceGate({}), /getBalanceUsdMicros is required/);
  });

  it("validates minBalanceUsdMicros and reauthTtlSeconds", () => {
    assert.throws(
      () => createBalanceGate({ getBalanceUsdMicros: async () => 0n, minBalanceUsdMicros: "x" }),
      /minBalanceUsdMicros must be an integer/,
    );
    assert.throws(
      () => createBalanceGate({ getBalanceUsdMicros: async () => 0n, reauthTtlSeconds: 0 }),
      /reauthTtlSeconds must be a positive number/,
    );
  });

  it("allows a positive balance", async () => {
    const gate = createBalanceGate({ getBalanceUsdMicros: async () => "1" });
    assert.equal(await gate(ctx()), undefined);
  });

  it("rejects zero balance with 483 insufficient_balance", async () => {
    const gate = createBalanceGate({ getBalanceUsdMicros: async () => 0n });
    await assert.rejects(gate(ctx()), (err) => {
      assert.ok(err instanceof WebhookError);
      assert.equal(err.status, 483);
      assert.equal(err.code, "insufficient_balance");
      return true;
    });
  });

  it("rejects a negative balance", async () => {
    const gate = createBalanceGate({ getBalanceUsdMicros: async () => "-100" });
    await assert.rejects(gate(ctx()), /insufficient balance/);
  });

  it("honors a custom minBalanceUsdMicros threshold", async () => {
    const gate = createBalanceGate({
      getBalanceUsdMicros: async () => 500n,
      minBalanceUsdMicros: 1000n,
    });
    await assert.rejects(gate(ctx()), (err) => err.status === 483);
  });

  it("passes identity to the lookup", async () => {
    let seen;
    const gate = createBalanceGate({
      getBalanceUsdMicros: async (id) => {
        seen = id;
        return 10n;
      },
    });
    await gate(ctx());
    assert.equal(seen.client_id, "tenant-a");
    assert.equal(seen.usage_subject, "user-1");
  });

  it("caps expiry via reauthTtlSeconds", async () => {
    const gate = createBalanceGate({
      getBalanceUsdMicros: async () => 10n,
      reauthTtlSeconds: 30,
    });
    const before = Math.floor(Date.now() / 1000);
    const result = await gate(ctx());
    assert.ok(result.expiry >= before + 30 && result.expiry <= before + 31);
  });

  it("fails closed with 503 on lookup error by default", async () => {
    const errors = [];
    const gate = createBalanceGate({
      getBalanceUsdMicros: async () => {
        throw new Error("openmeter down");
      },
      onError: (err) => errors.push(err),
    });
    await assert.rejects(gate(ctx()), (err) => {
      assert.equal(err.status, 503);
      assert.equal(err.code, "billing_unavailable");
      return true;
    });
    assert.equal(errors.length, 1);
  });

  it("fails open when failClosed is false", async () => {
    const gate = createBalanceGate({
      getBalanceUsdMicros: async () => {
        throw new Error("openmeter down");
      },
      failClosed: false,
    });
    assert.equal(await gate(ctx()), undefined);
  });

  it("treats an unparseable balance as billing_unavailable when failing closed", async () => {
    const gate = createBalanceGate({ getBalanceUsdMicros: async () => "not-a-number" });
    await assert.rejects(gate(ctx()), (err) => err.status === 503);
  });
});
