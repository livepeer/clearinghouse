import { createServer } from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { deriveKey } from "./crypto.mjs";
import { createStore } from "./store.mjs";
import { routeRequest } from "./routes.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.PORT || 8091);
const MAX_BODY_BYTES = 256 * 1024;

function required(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

const platformSecret = required("PLATFORM_API_SECRET");
const encryptionKey = deriveKey(required("CREDENTIALS_ENCRYPTION_KEY"));
const dataDir = process.env.DATA_DIR?.trim() || path.join(__dirname, "data");
const catalogPath = process.env.CATALOG_PATH?.trim() || path.join(__dirname, "catalog.json");
const identityBase = process.env.KONNECT_IDENTITY_BASE?.trim() || undefined;

const store = createStore({
  dataDir,
  encryptionKey,
});

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    req.on("data", (chunk) => {
      total += chunk.length;
      if (total > MAX_BODY_BYTES) {
        req.destroy();
        reject(new Error("payload too large"));
        return;
      }
      chunks.push(chunk);
    });
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

  let body;
  if (req.method !== "GET" && req.method !== "HEAD") {
    try {
      body = await readBody(req);
    } catch (err) {
      if (err.message === "payload too large") {
        res.writeHead(413, { "Content-Type": "text/plain" });
        res.end("payload too large");
        return;
      }
      throw err;
    }
  }

  const headers = new Headers();
  for (const [key, value] of Object.entries(req.headers)) {
    if (value === undefined) {
      continue;
    }
    headers.set(key, Array.isArray(value) ? value.join(", ") : value);
  }

  const request = new Request(new URL(req.url, `http://localhost:${port}`), {
    method: req.method,
    headers,
    body: body?.length ? body : undefined,
  });

  const response = await routeRequest(request, {
    store,
    platformSecret,
    catalogPath,
    identityBase,
  });

  if (!response) {
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "not_found" }));
    return;
  }

  const buf = Buffer.from(await response.arrayBuffer());
  const outHeaders = {};
  response.headers.forEach((v, k) => {
    outHeaders[k] = v;
  });
  res.writeHead(response.status, outHeaders);
  res.end(buf);
}

createServer((req, res) => {
  handleRequest(req, res).catch((err) => {
    console.error(err);
    if (!res.headersSent) {
      res.writeHead(500, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "internal_error" }));
    }
  });
}).listen(port, () => {
  console.log(`konnect-credentials listening on :${port}`);
});
