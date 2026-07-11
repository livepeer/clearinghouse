import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { deriveKey } from "../crypto.mjs";
import { createStore } from "../store.mjs";
import { routeRequest } from "../routes.mjs";

const SECRET = "platform-test-secret";

function mockFetchFactory(handlers) {
  return async (url, init = {}) => {
    const u = String(url);
    const method = (init.method || "GET").toUpperCase();
    for (const h of handlers) {
      if (h.match(u, method, init)) {
        return h.respond(u, method, init);
      }
    }
    return new Response(JSON.stringify({ message: `unhandled ${method} ${u}` }), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    });
  };
}

function jsonResponse(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("routes isolation and handoff", () => {
  let dir;
  let store;

  before(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "ch-routes-"));
    store = createStore({
      dataDir: dir,
      encryptionKey: deriveKey("route-test-key"),
    });
  });

  after(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("rejects unauthenticated requests", async () => {
    const res = await routeRequest(
      new Request("http://localhost/v1/tenants/x", { method: "GET" }),
      { store, platformSecret: SECRET },
    );
    assert.equal(res.status, 401);
  });

  it("binds two tenants to different orgs and keeps ingest isolated", async () => {
    const fetchImpl = mockFetchFactory([
      {
        match: (u, m) => m === "GET" && u.includes("/organizations/me"),
        respond: (_u, _m, init) => {
          const auth = init.headers.Authorization || "";
          if (auth.includes("token-a")) {
            return jsonResponse(200, { id: "org-aaa", name: "Tenant A Org" });
          }
          if (auth.includes("token-b")) {
            return jsonResponse(200, { id: "org-bbb", name: "Tenant B Org" });
          }
          return jsonResponse(401, { message: "bad token" });
        },
      },
    ]);

    const bindA = await routeRequest(
      new Request("http://localhost/v1/tenants/client-a/konnect/bind", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${SECRET}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ region: "us", admin_token: "kpat_token-a" }),
      }),
      { store, platformSecret: SECRET, fetchImpl },
    );
    assert.equal(bindA.status, 200);
    const bodyA = await bindA.json();
    assert.equal(bodyA.org_id, "org-aaa");

    const bindB = await routeRequest(
      new Request("http://localhost/v1/tenants/client-b/konnect/bind", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${SECRET}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ region: "eu", admin_token: "kpat_token-b" }),
      }),
      { store, platformSecret: SECRET, fetchImpl },
    );
    assert.equal(bindB.status, 200);
    const bodyB = await bindB.json();
    assert.equal(bodyB.org_id, "org-bbb");
    assert.equal(bodyB.region, "eu");

    // Simulate credentials issued with distinct ingest SPATs
    await store.upsertTenant("client-a", { ingest_spat: "spat_ingest_a" });
    await store.upsertTenant("client-b", { ingest_spat: "spat_ingest_b" });

    const ingestA = await routeRequest(
      new Request("http://localhost/v1/internal/tenants/client-a/ingest", {
        method: "GET",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET },
    );
    const ingestB = await routeRequest(
      new Request("http://localhost/v1/internal/tenants/client-b/ingest", {
        method: "GET",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET },
    );
    const ia = await ingestA.json();
    const ib = await ingestB.json();
    assert.equal(ia.token, "spat_ingest_a");
    assert.equal(ib.token, "spat_ingest_b");
    assert.equal(ia.url, "https://us.api.konghq.com/v3/openmeter/events");
    assert.equal(ib.url, "https://eu.api.konghq.com/v3/openmeter/events");
    assert.notEqual(ia.token, ib.token);

    const omA = await routeRequest(
      new Request("http://localhost/v1/internal/tenants/client-a/openmeter", {
        method: "GET",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET },
    );
    const omB = await routeRequest(
      new Request("http://localhost/v1/internal/tenants/client-b/openmeter", {
        method: "GET",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET },
    );
    assert.equal(omA.status, 200);
    assert.equal(omB.status, 200);
    const oa = await omA.json();
    const ob = await omB.json();
    assert.equal(oa.token, "kpat_token-a");
    assert.equal(ob.token, "kpat_token-b");
    assert.equal(oa.openmeter_base, "https://us.api.konghq.com/v3/openmeter");
    assert.equal(ob.openmeter_base, "https://eu.api.konghq.com/v3/openmeter");

    const unbound = await routeRequest(
      new Request("http://localhost/v1/internal/tenants/missing/openmeter", {
        method: "GET",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET },
    );
    assert.equal(unbound.status, 404);
  });

  it("issues credentials and does not return ingest secret", async () => {
    const accounts = [];
    const fetchImpl = mockFetchFactory([
      {
        match: (u, m) => m === "GET" && u.includes("/system-accounts") && !u.includes("assigned-roles") && !u.includes("access-tokens"),
        respond: () => jsonResponse(200, { data: accounts }),
      },
      {
        match: (u, m) => m === "POST" && /\/system-accounts$/.test(u.replace(/\?.*/, "")),
        respond: async (_u, _m, init) => {
          const body = JSON.parse(init.body);
          const row = { id: `sa-${body.name}`, name: body.name };
          accounts.push(row);
          return jsonResponse(201, row);
        },
      },
      {
        match: (u, m) => m === "GET" && u.includes("/assigned-roles"),
        respond: () => jsonResponse(200, { data: [] }),
      },
      {
        match: (u, m) => m === "POST" && u.includes("/assigned-roles"),
        respond: () => jsonResponse(201, { id: "role-1" }),
      },
      {
        match: (u, m) => m === "POST" && u.includes("/access-tokens"),
        respond: async (u) => {
          const accountId = u.match(/system-accounts\/([^/]+)/)[1];
          return jsonResponse(201, {
            id: `tok-${accountId}`,
            name: "t",
            token: `spat_${accountId}`,
          });
        },
      },
    ]);

    await store.upsertTenant("client-c", {
      region: "us",
      org_id: "org-c",
      admin_token: "kpat_c",
    });

    const res = await routeRequest(
      new Request("http://localhost/v1/tenants/client-c/konnect/credentials", {
        method: "POST",
        headers: { Authorization: `Bearer ${SECRET}` },
      }),
      { store, platformSecret: SECRET, fetchImpl },
    );
    assert.equal(res.status, 201);
    const body = await res.json();
    assert.ok(body.credentials.provisioner.token.startsWith("spat_"));
    assert.ok(body.credentials.usage.token.startsWith("spat_"));
    assert.equal(body.ingest.stored, true);
    assert.equal(body.credentials.ingest, undefined);

    const tenant = await store.getTenant("client-c");
    assert.ok(tenant.ingest_spat.startsWith("spat_"));
    assert.ok(tenant.token_ids.provisioner);
  });
});
