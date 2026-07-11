import path from "node:path";
import { fileURLToPath } from "node:url";
import { createKonnectClient, ingestUrl, unwrapOne } from "./konnect-client.mjs";
import { bootstrapCatalog } from "./catalog.mjs";
import { provisionCredentials, rotateCredential } from "./provision.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const DEFAULT_CATALOG = path.join(__dirname, "catalog.json");

const REGIONS = new Set(["us", "eu", "au", "in", "me", "sg"]);

/**
 * Route HTTP requests for the konnect-credentials service.
 * Returns a Response or null if unmatched.
 */
export async function routeRequest(request, {
  store,
  platformSecret,
  catalogPath = DEFAULT_CATALOG,
  fetchImpl = globalThis.fetch,
  identityBase,
}) {
  const url = new URL(request.url);
  const { pathname } = url;

  if (request.method === "GET" && pathname === "/health") {
    return text("ok", 200);
  }

  if (!authorizePlatform(request, platformSecret)) {
    return json({ error: "unauthorized" }, 401);
  }

  // GET /v1/internal/tenants/:clientId/ingest — collector lookup
  {
    const m = pathname.match(/^\/v1\/internal\/tenants\/([^/]+)\/ingest$/);
    if (m && request.method === "GET") {
      return getIngest(store, decodeURIComponent(m[1]));
    }
  }

  // POST /v1/tenants/:clientId/konnect/bind
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)\/konnect\/bind$/);
    if (m && request.method === "POST") {
      return bindTenant(store, decodeURIComponent(m[1]), request, {
        fetchImpl,
        identityBase,
      });
    }
  }

  // POST /v1/tenants/:clientId/konnect/credentials
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)\/konnect\/credentials$/);
    if (m && request.method === "POST") {
      return issueCredentials(store, decodeURIComponent(m[1]), {
        fetchImpl,
        identityBase,
      });
    }
  }

  // POST /v1/tenants/:clientId/konnect/credentials/rotate
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)\/konnect\/credentials\/rotate$/);
    if (m && request.method === "POST") {
      return rotate(store, decodeURIComponent(m[1]), request, {
        fetchImpl,
        identityBase,
      });
    }
  }

  // DELETE /v1/tenants/:clientId/konnect/credentials/:kind
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)\/konnect\/credentials\/(provisioner|usage|ingest)$/);
    if (m && request.method === "DELETE") {
      return revoke(store, decodeURIComponent(m[1]), m[2], {
        fetchImpl,
        identityBase,
      });
    }
  }

  // POST /v1/tenants/:clientId/konnect/catalog
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)\/konnect\/catalog$/);
    if (m && request.method === "POST") {
      return runCatalog(store, decodeURIComponent(m[1]), catalogPath, {
        fetchImpl,
        identityBase,
      });
    }
  }

  // GET /v1/tenants/:clientId
  {
    const m = pathname.match(/^\/v1\/tenants\/([^/]+)$/);
    if (m && request.method === "GET") {
      return getTenantPublic(store, decodeURIComponent(m[1]));
    }
  }

  return null;
}

function authorizePlatform(request, platformSecret) {
  const header = request.headers.get("authorization") || "";
  const apiKey = request.headers.get("x-api-key") || "";
  const bearer = header.startsWith("Bearer ") ? header.slice(7).trim() : "";
  const presented = bearer || apiKey;
  if (!platformSecret || !presented) {
    return false;
  }
  return timingSafeEqual(presented, platformSecret);
}

function timingSafeEqual(a, b) {
  if (a.length !== b.length) {
    return false;
  }
  let out = 0;
  for (let i = 0; i < a.length; i++) {
    out |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return out === 0;
}

async function bindTenant(store, clientId, request, opts) {
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "invalid_json" }, 400);
  }
  const region = String(body.region || "us").trim().toLowerCase();
  const adminToken = String(body.admin_token || "").trim();
  if (!REGIONS.has(region)) {
    return json({ error: "invalid_region", allowed: [...REGIONS] }, 400);
  }
  if (!adminToken) {
    return json({ error: "admin_token_required" }, 400);
  }

  const client = createKonnectClient({
    token: adminToken,
    region,
    identityBase: opts.identityBase,
    fetchImpl: opts.fetchImpl,
  });

  let org;
  try {
    org = unwrapOne(await client.getOrganizationMe());
  } catch (err) {
    return json({
      error: "konnect_auth_failed",
      detail: err.message,
    }, err.status === 401 || err.status === 403 ? 401 : 502);
  }

  const tenant = await store.upsertTenant(clientId, {
    region,
    org_id: org.id,
    org_name: org.name,
    admin_token: adminToken,
    bound_at: new Date().toISOString(),
  });

  return json({
    client_id: tenant.client_id,
    org_id: tenant.org_id,
    org_name: tenant.org_name,
    region: tenant.region,
    bound_at: tenant.bound_at,
  }, 200);
}

async function issueCredentials(store, clientId, opts) {
  const tenant = await store.getTenant(clientId);
  if (!tenant?.admin_token) {
    return json({ error: "tenant_not_bound" }, 404);
  }

  const client = createKonnectClient({
    token: tenant.admin_token,
    region: tenant.region,
    identityBase: opts.identityBase,
    fetchImpl: opts.fetchImpl,
  });

  let result;
  try {
    result = await provisionCredentials(client, {
      tokenNameSuffix: clientId.slice(0, 24),
    });
  } catch (err) {
    return json({ error: "provision_failed", detail: err.message }, 502);
  }

  await store.upsertTenant(clientId, {
    accounts: result.accounts,
    token_ids: {
      provisioner: result.tokens.provisioner.id,
      usage: result.tokens.usage.id,
      ingest: result.tokens.ingest.id,
    },
    ingest_spat: result.tokens.ingest.token,
    credentials_issued_at: new Date().toISOString(),
  });

  return json({
    client_id: clientId,
    org_id: tenant.org_id,
    region: tenant.region,
    openmeter_base: client.openMeterBase,
    accounts: result.accounts,
    // Secrets returned once — not stored for provisioner/usage.
    credentials: {
      provisioner: {
        token_id: result.tokens.provisioner.id,
        token: result.tokens.provisioner.token,
        roles: ["Billing Admin", "Product Catalog Admin", "Metering Admin"],
      },
      usage: {
        token_id: result.tokens.usage.id,
        token: result.tokens.usage.token,
        roles: ["Metering Viewer", "Billing Viewer"],
      },
    },
    // Ingest is platform-only; acknowledge issuance without returning secret.
    ingest: {
      token_id: result.tokens.ingest.id,
      stored: true,
    },
  }, 201);
}

async function rotate(store, clientId, request, opts) {
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "invalid_json" }, 400);
  }
  const kind = String(body.kind || "").trim();
  if (!["provisioner", "usage", "ingest"].includes(kind)) {
    return json({ error: "invalid_kind", allowed: ["provisioner", "usage", "ingest"] }, 400);
  }

  const tenant = await store.getTenant(clientId);
  if (!tenant?.admin_token || !tenant.accounts?.[kind]?.id) {
    return json({ error: "credentials_not_issued" }, 404);
  }

  const client = createKonnectClient({
    token: tenant.admin_token,
    region: tenant.region,
    identityBase: opts.identityBase,
    fetchImpl: opts.fetchImpl,
  });

  let rotated;
  try {
    rotated = await rotateCredential(client, {
      kind,
      accountId: tenant.accounts[kind].id,
      previousTokenId: tenant.token_ids?.[kind],
    });
  } catch (err) {
    return json({ error: "rotate_failed", detail: err.message }, 502);
  }

  const tokenIds = { ...(tenant.token_ids || {}), [kind]: rotated.id };
  const patch = { token_ids: tokenIds };
  if (kind === "ingest") {
    patch.ingest_spat = rotated.token;
  }
  await store.upsertTenant(clientId, patch);

  if (kind === "ingest") {
    return json({
      client_id: clientId,
      kind,
      token_id: rotated.id,
      stored: true,
    }, 200);
  }

  return json({
    client_id: clientId,
    kind,
    token_id: rotated.id,
    token: rotated.token,
  }, 200);
}

async function revoke(store, clientId, kind, opts) {
  const tenant = await store.getTenant(clientId);
  if (!tenant?.admin_token || !tenant.accounts?.[kind]?.id) {
    return json({ error: "credentials_not_issued" }, 404);
  }
  const tokenId = tenant.token_ids?.[kind];
  if (!tokenId) {
    return json({ error: "token_id_unknown" }, 404);
  }

  const client = createKonnectClient({
    token: tenant.admin_token,
    region: tenant.region,
    identityBase: opts.identityBase,
    fetchImpl: opts.fetchImpl,
  });

  try {
    await client.deleteAccessToken(tenant.accounts[kind].id, tokenId);
  } catch (err) {
    if (err.status !== 404) {
      return json({ error: "revoke_failed", detail: err.message }, 502);
    }
  }

  const tokenIds = { ...(tenant.token_ids || {}) };
  delete tokenIds[kind];
  const patch = { token_ids: tokenIds };
  if (kind === "ingest") {
    patch.ingest_spat = null;
  }
  await store.upsertTenant(clientId, patch);

  return json({ client_id: clientId, kind, revoked: true }, 200);
}

async function runCatalog(store, clientId, catalogPath, opts) {
  const tenant = await store.getTenant(clientId);
  if (!tenant?.admin_token) {
    return json({ error: "tenant_not_bound" }, 404);
  }

  const client = createKonnectClient({
    token: tenant.admin_token,
    region: tenant.region,
    identityBase: opts.identityBase,
    fetchImpl: opts.fetchImpl,
  });

  try {
    const result = await bootstrapCatalog(client, catalogPath);
    await store.upsertTenant(clientId, {
      catalog_bootstrapped_at: new Date().toISOString(),
    });
    return json({
      client_id: clientId,
      org_id: tenant.org_id,
      region: tenant.region,
      catalog: result,
    }, 200);
  } catch (err) {
    return json({ error: "catalog_failed", detail: err.message }, 502);
  }
}

async function getIngest(store, clientId) {
  const tenant = await store.getTenant(clientId);
  if (!tenant?.ingest_spat) {
    return json({ error: "ingest_unavailable" }, 404);
  }
  return json({
    client_id: clientId,
    region: tenant.region,
    org_id: tenant.org_id,
    url: ingestUrl(tenant.region),
    token: tenant.ingest_spat,
  }, 200);
}

async function getTenantPublic(store, clientId) {
  const tenant = await store.getTenant(clientId);
  if (!tenant) {
    return json({ error: "not_found" }, 404);
  }
  return json({
    client_id: tenant.client_id,
    org_id: tenant.org_id,
    org_name: tenant.org_name,
    region: tenant.region,
    bound_at: tenant.bound_at,
    credentials_issued_at: tenant.credentials_issued_at,
    catalog_bootstrapped_at: tenant.catalog_bootstrapped_at,
    accounts: tenant.accounts || null,
    token_ids: tenant.token_ids || null,
    has_ingest: Boolean(tenant.ingest_spat),
  }, 200);
}

function json(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function text(body, status = 200) {
  return new Response(body, {
    status,
    headers: { "Content-Type": "text/plain" },
  });
}
