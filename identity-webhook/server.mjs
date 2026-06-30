import { createServer } from "node:http";
import { routeWebhookRequest } from "./protocol.mjs";
import { createEndUserVerifierFromEnv } from "./verifiers.mjs";

const port = Number(process.env.PORT || 8090);
const MAX_BODY_BYTES = 64 * 1024;

function required(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

const endUserAuth = createEndUserVerifierFromEnv(process.env);

const config = {
  webhookSecret: required("WEBHOOK_SECRET"),
  endUserAuth,
};

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
  console.log(
    `identity-webhook (jose, ${process.env.IDENTITY_AUTH_MODE?.trim()}) listening on :${port}`,
  );
});
