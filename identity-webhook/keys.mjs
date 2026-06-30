/**
 * Load demo API keys from env for the in-compose identity webhook.
 *
 * DEMO_API_KEY + DEMO_CLIENT_ID + DEMO_USER_ID define one key.
 * Optional DEMO_API_KEYS JSON map for multiple keys:
 *   {"sk_other":{"clientId":"app-b","userId":"user-b"}}
 */
export function loadApiKeyStore(env) {
  const store = new Map();

  const primaryKey = env.DEMO_API_KEY?.trim();
  if (primaryKey) {
    store.set(primaryKey, {
      clientId: env.DEMO_CLIENT_ID?.trim() || "demo-client",
      userId: env.DEMO_USER_ID?.trim() || "demo-user",
      usageSubjectType: env.USAGE_SUBJECT_TYPE?.trim() || "api_key_user",
    });
  }

  const extra = env.DEMO_API_KEYS?.trim();
  if (extra) {
    let parsed;
    try {
      parsed = JSON.parse(extra);
    } catch {
      throw new Error("DEMO_API_KEYS must be valid JSON");
    }
    if (parsed && typeof parsed === "object") {
      for (const [apiKey, entry] of Object.entries(parsed)) {
        if (!apiKey?.trim() || !entry || typeof entry !== "object") {
          continue;
        }
        const userId = entry.userId?.trim() || entry.user_id?.trim();
        if (!userId) {
          continue;
        }
        store.set(apiKey.trim(), {
          clientId: entry.clientId?.trim() || entry.client_id?.trim() || "demo-client",
          userId,
          usageSubjectType:
            entry.usageSubjectType?.trim() ||
            entry.usage_subject_type?.trim() ||
            "api_key_user",
        });
      }
    }
  }

  if (store.size === 0) {
    throw new Error("DEMO_API_KEY or DEMO_API_KEYS is required");
  }

  return store;
}
