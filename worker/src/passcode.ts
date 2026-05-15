// Passcode hash + cookie helpers.
//
// PASSCODE HASHING
// ----------------
// SHA-256 over `<passcode>|<salt>`, hex-encoded. Cross-pinned with
// lambdas/pkg/passcode/passcode.go:Hash via the
// "Hash drift — Worker would mis-validate" assertion on both sides.
//
// argon2id swap stays deferred: hash-wasm and similar Workers-side
// libraries compile WebAssembly at runtime via WebAssembly.compile(),
// which the cloudflare/vitest-pool-workers test runtime disallows
// without a preloaded `wasm_modules` wrangler binding. We tried
// pulling hash-wasm in 5.4; production runs fine but the worker test
// suite errored with "Wasm code generation disallowed by embedder".
// The Lambda side (Go, argon2 stdlib-adjacent via x/crypto/argon2)
// works cleanly. Until we can either preload the WASM module via
// wrangler's wasm_modules feature or move the worker tests off the
// workers pool, both sides stay on SHA-256.
//
// COOKIE
// ------
// HMAC-SHA256 signed cookie scoped to /sites/<websiteId>/, ` Secure; HttpOnly;
// SameSite=Lax`, 24h TTL. Body is `<websiteId>.<exp_unix>.<sig_hex>`. Validation
// recomputes the HMAC and constant-time-compares; rejects if expired.

export const COOKIE_NAME = "preview_session";
const COOKIE_TTL_SECONDS = 24 * 60 * 60;

const enc = new TextEncoder();

// ---------- passcode ----------

export async function hashPasscode(passcode: string, salt: string): Promise<string> {
  if (passcode === "") return "";
  const buf = await crypto.subtle.digest("SHA-256", enc.encode(`${passcode}|${salt}`));
  return toHex(buf);
}

export async function validatePasscode(
  submitted: string,
  storedHash: string,
  salt: string,
): Promise<boolean> {
  const computed = await hashPasscode(submitted, salt);
  return constantTimeEqual(computed, storedHash);
}

// ---------- cookie ----------

export async function signCookie(websiteId: string, salt: string): Promise<string> {
  const exp = Math.floor(Date.now() / 1000) + COOKIE_TTL_SECONDS;
  const payload = `${websiteId}.${exp}`;
  const sig = await hmacHex(salt, payload);
  const value = `${payload}.${sig}`;
  return `${COOKIE_NAME}=${encodeURIComponent(value)}; Path=/sites/${websiteId}/; Secure; HttpOnly; SameSite=Lax; Max-Age=${COOKIE_TTL_SECONDS}`;
}

export async function verifyCookie(cookieHeader: string, websiteId: string, salt: string): Promise<boolean> {
  const cookies = parseCookies(cookieHeader);
  const raw = cookies[COOKIE_NAME];
  if (!raw) return false;
  const decoded = decodeURIComponent(raw);
  const parts = decoded.split(".");
  if (parts.length !== 3) return false;
  const [cookieWebsiteId, expStr, sigHex] = parts;
  if (cookieWebsiteId !== websiteId) return false;
  const exp = Number(expStr);
  if (!Number.isFinite(exp) || exp <= Math.floor(Date.now() / 1000)) return false;
  const expectedSig = await hmacHex(salt, `${cookieWebsiteId}.${exp}`);
  return constantTimeEqual(sigHex, expectedSig);
}

// ---------- helpers ----------

async function hmacHex(secret: string, msg: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const sig = await crypto.subtle.sign("HMAC", key, enc.encode(msg));
  return toHex(sig);
}

function toHex(buf: ArrayBuffer): string {
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

// Constant-time string comparison. Inputs are hex strings of equal length
// when produced by our own helpers; if lengths differ we still iterate over
// the longer to avoid early-exit timing leaks.
function constantTimeEqual(a: string, b: string): boolean {
  const len = Math.max(a.length, b.length);
  let diff = a.length === b.length ? 0 : 1;
  for (let i = 0; i < len; i++) {
    diff |= (a.charCodeAt(i) || 0) ^ (b.charCodeAt(i) || 0);
  }
  return diff === 0;
}

function parseCookies(header: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of header.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    const k = part.slice(0, eq).trim();
    const v = part.slice(eq + 1).trim();
    if (k) out[k] = v;
  }
  return out;
}
