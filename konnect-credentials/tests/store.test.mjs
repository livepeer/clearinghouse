import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { deriveKey } from "../crypto.mjs";
import { createStore } from "../store.mjs";

describe("store encryption and isolation", () => {
  let dir;
  let store;

  before(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "ch-konnect-"));
    store = createStore({
      dataDir: dir,
      encryptionKey: deriveKey("test-encryption-passphrase"),
    });
  });

  after(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("encrypts admin_token and ingest_spat at rest", async () => {
    await store.upsertTenant("app-a", {
      region: "us",
      org_id: "org-a",
      admin_token: "kpat_secret_a",
      ingest_spat: "spat_ingest_a",
    });
    const row = await store.getTenant("app-a");
    assert.equal(row.admin_token, "kpat_secret_a");
    assert.equal(row.ingest_spat, "spat_ingest_a");
    assert.ok(row.admin_token_enc);
    assert.notEqual(row.admin_token_enc, "kpat_secret_a");
  });

  it("isolates tenants by client_id", async () => {
    await store.upsertTenant("app-a", {
      region: "us",
      org_id: "org-a",
      admin_token: "kpat_a",
      ingest_spat: "spat_a",
    });
    await store.upsertTenant("app-b", {
      region: "eu",
      org_id: "org-b",
      admin_token: "kpat_b",
      ingest_spat: "spat_b",
    });

    const a = await store.getTenant("app-a");
    const b = await store.getTenant("app-b");
    assert.equal(a.org_id, "org-a");
    assert.equal(b.org_id, "org-b");
    assert.equal(a.ingest_spat, "spat_a");
    assert.equal(b.ingest_spat, "spat_b");
    assert.notEqual(a.admin_token, b.admin_token);
  });

  it("does not leak secrets across tenants on list", async () => {
    const listed = await store.listTenants();
    const ids = listed.map((t) => t.client_id).sort();
    assert.deepEqual(ids, ["app-a", "app-b"]);
    for (const t of listed) {
      assert.ok(t.ingest_spat.startsWith("spat_"));
      assert.ok(t.org_id.startsWith("org-"));
    }
  });
});
