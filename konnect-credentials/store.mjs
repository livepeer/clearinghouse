import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { decrypt, encrypt } from "./crypto.mjs";

/**
 * File-backed tenant store. Secrets (admin_token, ingest_spat) are encrypted at rest.
 * Tenant provisioner/usage SPATs are never persisted after handoff — only token IDs.
 */
export function createStore({
  dataDir,
  encryptionKey,
}) {
  const filePath = path.join(dataDir, "tenants.json");
  let cache = null;

  async function ensureLoaded() {
    if (cache) {
      return cache;
    }
    await mkdir(dataDir, { recursive: true });
    try {
      const raw = await readFile(filePath, "utf8");
      cache = JSON.parse(raw);
    } catch (err) {
      if (err.code === "ENOENT") {
        cache = { tenants: {} };
      } else {
        throw err;
      }
    }
    return cache;
  }

  async function persist() {
    const data = await ensureLoaded();
    const tmp = `${filePath}.${process.pid}.tmp`;
    await writeFile(tmp, `${JSON.stringify(data, null, 2)}\n`, "utf8");
    await rename(tmp, filePath);
  }

  function seal(value) {
    if (value == null || value === "") {
      return null;
    }
    return encrypt(value, encryptionKey);
  }

  function open(value) {
    if (value == null || value === "") {
      return null;
    }
    return decrypt(value, encryptionKey);
  }

  return {
    async getTenant(clientId) {
      const data = await ensureLoaded();
      const row = data.tenants[clientId];
      if (!row) {
        return null;
      }
      return hydrate(row, open);
    },

    async listTenants() {
      const data = await ensureLoaded();
      return Object.keys(data.tenants).sort().map((id) => hydrate(data.tenants[id], open));
    },

    async upsertTenant(clientId, patch) {
      const data = await ensureLoaded();
      const prev = data.tenants[clientId] || {
        client_id: clientId,
        created_at: new Date().toISOString(),
      };
      const next = {
        ...prev,
        ...stripSecrets(patch),
        client_id: clientId,
        updated_at: new Date().toISOString(),
      };
      if (Object.prototype.hasOwnProperty.call(patch, "admin_token")) {
        next.admin_token_enc = seal(patch.admin_token);
      }
      if (Object.prototype.hasOwnProperty.call(patch, "ingest_spat")) {
        next.ingest_spat_enc = seal(patch.ingest_spat);
      }
      data.tenants[clientId] = next;
      await persist();
      return hydrate(next, open);
    },

    async deleteTenant(clientId) {
      const data = await ensureLoaded();
      if (!data.tenants[clientId]) {
        return false;
      }
      delete data.tenants[clientId];
      await persist();
      return true;
    },
  };
}

function stripSecrets(patch) {
  const {
    admin_token: _a,
    ingest_spat: _i,
    ...rest
  } = patch;
  return rest;
}

function hydrate(row, open) {
  return {
    ...row,
    admin_token: open(row.admin_token_enc),
    ingest_spat: open(row.ingest_spat_enc),
  };
}
