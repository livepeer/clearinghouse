/**
 * Thin Konnect Identity + OpenMeter HTTP client.
 * Identity: https://global.api.konghq.com/v2
 * OpenMeter: https://{region}.api.konghq.com/v3/openmeter
 */

const DEFAULT_IDENTITY_BASE = "https://global.api.konghq.com/v2";

export function openMeterBase(region) {
  const r = String(region || "us").trim().toLowerCase();
  return `https://${r}.api.konghq.com/v3/openmeter`;
}

export function ingestUrl(region) {
  return `${openMeterBase(region)}/events`;
}

export function createKonnectClient({
  token,
  region = "us",
  identityBase = DEFAULT_IDENTITY_BASE,
  fetchImpl = globalThis.fetch,
}) {
  if (!token) {
    throw new Error("Konnect token is required");
  }

  async function request(base, method, path, body) {
    const url = `${base.replace(/\/$/, "")}${path.startsWith("/") ? path : `/${path}`}`;
    const headers = {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
    };
    let payload;
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      payload = JSON.stringify(body);
    }
    const res = await fetchImpl(url, {
      method,
      headers,
      body: payload,
    });
    const text = await res.text();
    let json = null;
    if (text) {
      try {
        json = JSON.parse(text);
      } catch {
        json = { raw: text };
      }
    }
    if (!res.ok) {
      const err = new Error(
        `Konnect ${method} ${path} failed: ${res.status} ${summarizeError(json)}`,
      );
      err.status = res.status;
      err.body = json;
      throw err;
    }
    return json;
  }

  const identity = (method, path, body) => request(identityBase, method, path, body);
  const openmeter = (method, path, body) =>
    request(openMeterBase(region), method, path, body);

  return {
    region,
    identityBase,
    openMeterBase: openMeterBase(region),
    ingestUrl: ingestUrl(region),

    getOrganizationMe() {
      return identity("GET", "/organizations/me");
    },

    listSystemAccounts() {
      return identity("GET", "/system-accounts");
    },

    createSystemAccount({
      name,
      description,
    }) {
      return identity("POST", "/system-accounts", {
        name,
        description,
      });
    },

    deleteSystemAccount(accountId) {
      return identity("DELETE", `/system-accounts/${accountId}`);
    },

    listAssignedRoles(accountId) {
      return identity("GET", `/system-accounts/${accountId}/assigned-roles`);
    },

    assignRole(accountId, assignment) {
      return identity("POST", `/system-accounts/${accountId}/assigned-roles`, assignment);
    },

    createAccessToken(accountId, {
      name,
    }) {
      return identity("POST", `/system-accounts/${accountId}/access-tokens`, {
        name,
      });
    },

    deleteAccessToken(accountId, tokenId) {
      return identity("DELETE", `/system-accounts/${accountId}/access-tokens/${tokenId}`);
    },

    listAccessTokens(accountId) {
      return identity("GET", `/system-accounts/${accountId}/access-tokens`);
    },

    listMeters() {
      return openmeter("GET", "/meters");
    },

    createMeter(body) {
      return openmeter("POST", "/meters", body);
    },

    listFeatures() {
      return openmeter("GET", "/features");
    },

    createFeature(body) {
      return openmeter("POST", "/features", body);
    },

    listPlans() {
      return openmeter("GET", "/plans");
    },

    createPlan(body) {
      return openmeter("POST", "/plans", body);
    },

    publishPlan(planId) {
      return openmeter("POST", `/plans/${planId}/publish`, {});
    },
  };
}

function summarizeError(json) {
  if (!json || typeof json !== "object") {
    return "";
  }
  return json.message || json.detail || json.title || JSON.stringify(json).slice(0, 200);
}

/** Unwrap Konnect list envelopes: { data: [...] } or bare array. */
export function unwrapList(payload) {
  if (Array.isArray(payload)) {
    return payload;
  }
  if (payload && Array.isArray(payload.data)) {
    return payload.data;
  }
  if (payload && Array.isArray(payload.items)) {
    return payload.items;
  }
  return [];
}

export function unwrapOne(payload) {
  if (!payload || typeof payload !== "object") {
    return payload;
  }
  if (payload.data && typeof payload.data === "object" && !Array.isArray(payload.data)) {
    return payload.data;
  }
  return payload;
}
