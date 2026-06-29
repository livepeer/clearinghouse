import { createServer } from "node:http";
import { routeWebhookRequest } from "./protocol.mjs";
import {
  createApiKeyVerifier,
  createFirstMatchVerifier,
  createOidcVerifier,
} from "./verifiers.mjs";
import { loadApiKeyStore } from "./keys.mjs";

const port = Number(process.env.PORT || 8090);

function required(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function optional(name, fallback) {
  return process.env[name]?.trim() || fallback;
}

/**
 * Build the end-user verifier from env. OIDC is enabled when OIDC_ISSUER is set;
 * API keys when DEMO_API_KEY(S) is set; both → composite (JWT tried first).
 */
function buildEndUserVerifier() {
  const issuer = required("IDENTITY_ISSUER");
  const verifiers = [];

  if (process.env.OIDC_ISSUER?.trim()) {
    verifiers.push(
      createOidcVerifier({
        jwtIssuer: required("OIDC_ISSUER"),
        jwtAudience: required("OIDC_AUDIENCE"),
        jwksUri: process.env.OIDC_JWKS_URI?.trim(),
        issuer,
        clientClaim: optional("OIDC_CLIENT_CLAIM", "azp"),
        subjectClaim: optional("OIDC_SUBJECT_CLAIM", "sub"),
        subjectTypeValue: optional("OIDC_SUBJECT_TYPE", "oidc_user"),
        requiredScopes: (process.env.OIDC_REQUIRED_SCOPES?.trim() || "")
          .split(/[\s,]+/)
          .filter(Boolean),
      }),
    );
  }

  if (process.env.DEMO_API_KEY?.trim() || process.env.DEMO_API_KEYS?.trim()) {
    const keyStore = loadApiKeyStore(process.env);
    verifiers.push(
      createApiKeyVerifier({
        issuer,
        apiKeyPrefix: optional("API_KEY_PREFIX", "sk_"),
        defaultClientId: optional("DEMO_CLIENT_ID", "demo-client"),
        defaultUsageSubjectType: optional("USAGE_SUBJECT_TYPE", "api_key_user"),
        resolveApiKey: async (apiKey) => keyStore.get(apiKey) ?? null,
      }),
    );
  }

  if (verifiers.length === 0) {
    throw new Error("configure OIDC_ISSUER and/or DEMO_API_KEY(S)");
  }
  return verifiers.length === 1 ? verifiers[0] : createFirstMatchVerifier(verifiers);
}

const config = {
  webhookSecret: required("WEBHOOK_SECRET"),
  endUserAuth: buildEndUserVerifier(),
};

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

async function handleRequest(req, res) {
  if (req.method === "GET" && req.url === "/health") {
    res.writeHead(200, { "Content-Type": "text/plain" });
    res.end("ok");
    return;
  }

  const host = req.headers.host || `localhost:${port}`;
  const body =
    req.method === "GET" || req.method === "HEAD" ? undefined : await readBody(req);
  const headers = new Headers();
  for (const [key, value] of Object.entries(req.headers)) {
    if (value === undefined) {
      continue;
    }
    headers.set(key, Array.isArray(value) ? value.join(", ") : value);
  }

  const request = new Request(`http://${host}${req.url}`, {
    method: req.method,
    headers,
    body: body?.length ? body : undefined,
  });

  const response = await routeWebhookRequest(request, config);
  if (!response) {
    res.writeHead(404);
    res.end();
    return;
  }

  res.writeHead(response.status, Object.fromEntries(response.headers));
  res.end(await response.text());
}

createServer((req, res) => {
  handleRequest(req, res).catch((err) => {
    console.error("identity-webhook error:", err);
    if (!res.headersSent) {
      res.writeHead(500, { "Content-Type": "text/plain" });
    }
    res.end("internal error");
  });
}).listen(port, "0.0.0.0", () => {
  const modes = [];
  if (process.env.OIDC_ISSUER?.trim()) modes.push("oidc");
  if (process.env.DEMO_API_KEY?.trim() || process.env.DEMO_API_KEYS?.trim())
    modes.push("api-key");
  console.log(`identity-webhook (jose, ${modes.join("+")}) listening on :${port}`);
});
