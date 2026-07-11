import { createCipheriv, createDecipheriv, createHash, randomBytes } from "node:crypto";

const ALGO = "aes-256-gcm";
const IV_LEN = 12;
const TAG_LEN = 16;
const KEY_LEN = 32;

/**
 * Derive a 32-byte key from CREDENTIALS_ENCRYPTION_KEY.
 * Accepts 64-char hex, base64 (32 bytes), or any passphrase (SHA-256).
 */
export function deriveKey(secret) {
  const raw = String(secret || "").trim();
  if (!raw) {
    throw new Error("CREDENTIALS_ENCRYPTION_KEY is required");
  }
  if (/^[0-9a-fA-F]{64}$/.test(raw)) {
    return Buffer.from(raw, "hex");
  }
  try {
    const b64 = Buffer.from(raw, "base64");
    if (b64.length === KEY_LEN) {
      return b64;
    }
  } catch {
    // fall through to hash
  }
  return createHash("sha256").update(raw, "utf8").digest();
}

export function encrypt(plaintext, key) {
  const iv = randomBytes(IV_LEN);
  const cipher = createCipheriv(ALGO, key, iv);
  const enc = Buffer.concat([
    cipher.update(String(plaintext), "utf8"),
    cipher.final(),
  ]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([iv, tag, enc]).toString("base64");
}

export function decrypt(payload, key) {
  const buf = Buffer.from(String(payload), "base64");
  if (buf.length < IV_LEN + TAG_LEN + 1) {
    throw new Error("invalid ciphertext");
  }
  const iv = buf.subarray(0, IV_LEN);
  const tag = buf.subarray(IV_LEN, IV_LEN + TAG_LEN);
  const data = buf.subarray(IV_LEN + TAG_LEN);
  const decipher = createDecipheriv(ALGO, key, iv);
  decipher.setAuthTag(tag);
  return Buffer.concat([
    decipher.update(data),
    decipher.final(),
  ]).toString("utf8");
}
