import { describe, it, expect } from "vitest";
import { hashPasscode, validatePasscode, signCookie, verifyCookie, COOKIE_NAME } from "../src/passcode";
import worker from "../src/index";

const SALT = "test-salt-not-real";

describe("passcode hashing", () => {
  it("produces a stable hex hash", async () => {
    const a = await hashPasscode("ABC23456", SALT);
    const b = await hashPasscode("ABC23456", SALT);
    expect(a).toEqual(b);
    expect(a).toMatch(/^[0-9a-f]{64}$/);
  });

  it("validates a correct passcode", async () => {
    const stored = await hashPasscode("XYZ78901", SALT);
    expect(await validatePasscode("XYZ78901", stored, SALT)).toBe(true);
  });

  it("rejects an incorrect passcode", async () => {
    const stored = await hashPasscode("AAAAAAAA", SALT);
    expect(await validatePasscode("BBBBBBBB", stored, SALT)).toBe(false);
  });

  it("rejects when the salt differs", async () => {
    const stored = await hashPasscode("CCCCCCCC", SALT);
    expect(await validatePasscode("CCCCCCCC", stored, "wrong-salt")).toBe(false);
  });
});

describe("signed cookie", () => {
  it("round-trips a valid cookie", async () => {
    const setCookie = await signCookie("site-abc", SALT);
    expect(setCookie).toContain(`${COOKIE_NAME}=`);
    expect(setCookie).toContain("Path=/sites/site-abc/");
    expect(setCookie).toContain("Secure");
    expect(setCookie).toContain("HttpOnly");
    expect(setCookie).toContain("SameSite=Lax");

    // Build the corresponding Cookie header (just the name=value pair).
    const cookieHeader = setCookie.split(";")[0];
    expect(await verifyCookie(cookieHeader, "site-abc", SALT)).toBe(true);
  });

  it("rejects a cookie scoped to a different websiteId", async () => {
    const setCookie = await signCookie("site-abc", SALT);
    const cookieHeader = setCookie.split(";")[0];
    expect(await verifyCookie(cookieHeader, "site-xyz", SALT)).toBe(false);
  });

  it("rejects a cookie with a wrong salt", async () => {
    const setCookie = await signCookie("site-abc", SALT);
    const cookieHeader = setCookie.split(";")[0];
    expect(await verifyCookie(cookieHeader, "site-abc", "different-salt")).toBe(false);
  });

  it("rejects a tampered signature", async () => {
    const setCookie = await signCookie("site-abc", SALT);
    const cookieHeader = setCookie.split(";")[0];
    // Flip last hex char of the signature.
    const tampered = cookieHeader.replace(/.$/, (c) => (c === "0" ? "1" : "0"));
    expect(await verifyCookie(tampered, "site-abc", SALT)).toBe(false);
  });

  it("returns false on missing cookie", async () => {
    expect(await verifyCookie("", "site-abc", SALT)).toBe(false);
    expect(await verifyCookie("other=value", "site-abc", SALT)).toBe(false);
  });
});

describe("response headers", () => {
  // Mock env — only PASSCODE_SALT is needed for the routes we hit here.
  const env = {
    PASSCODE_SALT: SALT,
    ENVIRONMENT: "test",
    PREVIEWS: {} as R2Bucket,
    PREVIEW_PASSCODES_KV: {
      get: async () => null,
    } as unknown as KVNamespace,
  };

  it("/healthz carries X-Robots-Tag noindex,nofollow", async () => {
    const res = await worker.fetch(new Request("https://example.test/healthz"), env, {} as ExecutionContext);
    expect(res.headers.get("x-robots-tag")).toBe("noindex, nofollow");
  });

  it("passcode form response carries X-Robots-Tag noindex,nofollow", async () => {
    const res = await worker.fetch(
      new Request("https://example.test/sites/abc"),
      env,
      {} as ExecutionContext,
    );
    expect(res.headers.get("x-robots-tag")).toBe("noindex, nofollow");
    expect(res.headers.get("content-type")).toContain("text/html");
  });

  it("404 response carries X-Robots-Tag noindex,nofollow", async () => {
    const res = await worker.fetch(new Request("https://example.test/nope"), env, {} as ExecutionContext);
    expect(res.status).toBe(404);
    expect(res.headers.get("x-robots-tag")).toBe("noindex, nofollow");
  });
});

import { passcodeStillIssued, resetRevocationCacheForTests } from "../src/index";

describe("revocation propagation (iter 5.4)", () => {
  // Build a mutable KV mock so we can flip the stored value between calls
  // without rebuilding the whole env.
  function envWithKV(initial: string | null) {
    let stored: string | null = initial;
    const kv = {
      get: async (_key: string) => stored,
      set: (v: string | null) => {
        stored = v;
      },
    };
    return {
      env: {
        PASSCODE_SALT: SALT,
        ENVIRONMENT: "test",
        PREVIEWS: {} as R2Bucket,
        PREVIEW_PASSCODES_KV: kv as unknown as KVNamespace,
      },
      mutateKV: (v: string | null) => kv.set(v),
    };
  }

  it("returns true when KV has a hash for the websiteId", async () => {
    resetRevocationCacheForTests();
    const { env } = envWithKV("some-hash");
    expect(await passcodeStillIssued(env, "site-1")).toBe(true);
  });

  it("returns false when KV has no entry (revoked)", async () => {
    resetRevocationCacheForTests();
    const { env } = envWithKV(null);
    expect(await passcodeStillIssued(env, "site-1")).toBe(false);
  });

  it("treats an empty-string KV value as revoked", async () => {
    resetRevocationCacheForTests();
    const { env } = envWithKV("");
    expect(await passcodeStillIssued(env, "site-1")).toBe(false);
  });

  it("caches the result across rapid calls (within 60s)", async () => {
    resetRevocationCacheForTests();
    let getCalls = 0;
    const env = {
      PASSCODE_SALT: SALT,
      ENVIRONMENT: "test",
      PREVIEWS: {} as R2Bucket,
      PREVIEW_PASSCODES_KV: {
        get: async () => {
          getCalls++;
          return "hash";
        },
      } as unknown as KVNamespace,
    };
    await passcodeStillIssued(env, "site-cache");
    await passcodeStillIssued(env, "site-cache");
    await passcodeStillIssued(env, "site-cache");
    expect(getCalls).toBe(1);
  });

  it("cookie + revoked KV → /sites returns the passcode form", async () => {
    resetRevocationCacheForTests();
    const { env } = envWithKV(null); // revoked

    // Forge a valid signed cookie.
    const setCookie = await signCookie("site-revoked", SALT);
    const cookieHeader = setCookie.split(";")[0];

    const res = await worker.fetch(
      new Request("https://example.test/sites/site-revoked", {
        headers: { cookie: cookieHeader },
      }),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    const body = await res.text();
    expect(body).toContain("This preview has been revoked.");
  });

  it("cookie + revoked KV → /screenshots returns 403", async () => {
    resetRevocationCacheForTests();
    const { env } = envWithKV(null);

    const setCookie = await signCookie("site-revoked-shot", SALT);
    const cookieHeader = setCookie.split(";")[0];

    const res = await worker.fetch(
      new Request("https://example.test/screenshots/site-revoked-shot/thumb.png", {
        headers: { cookie: cookieHeader },
      }),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(403);
  });
});
