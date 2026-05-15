import { describe, it, expect } from "vitest";
import {
  hashPasscode,
  validatePasscode,
  signCookie,
  verifyCookie,
  signOpToken,
  verifyOpToken,
  COOKIE_NAME,
} from "../src/passcode";
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

describe("operator bypass token (iter 5.5)", () => {
  it("round-trips a valid token", async () => {
    const token = await signOpToken("site-op", SALT);
    expect(token.split(".")).toHaveLength(3);
    expect(token.startsWith("site-op.")).toBe(true);
    expect(await verifyOpToken(token, "site-op", SALT)).toBe(true);
  });

  it("rejects a token bound to a different websiteId", async () => {
    const token = await signOpToken("site-op", SALT);
    expect(await verifyOpToken(token, "site-other", SALT)).toBe(false);
  });

  it("rejects a token signed with a different salt", async () => {
    const token = await signOpToken("site-op", SALT);
    expect(await verifyOpToken(token, "site-op", "different-salt")).toBe(false);
  });

  it("rejects a tampered signature", async () => {
    const token = await signOpToken("site-op", SALT);
    const tampered = token.replace(/.$/, (c) => (c === "0" ? "1" : "0"));
    expect(await verifyOpToken(tampered, "site-op", SALT)).toBe(false);
  });

  it("rejects an expired token", async () => {
    // exp in the past; signature is irrelevant because expiry is checked first.
    expect(await verifyOpToken("site-op.1000.deadbeef", "site-op", SALT)).toBe(false);
  });

  it("rejects malformed tokens", async () => {
    expect(await verifyOpToken("", "site-op", SALT)).toBe(false);
    expect(await verifyOpToken("a.b", "site-op", SALT)).toBe(false);
    expect(await verifyOpToken("a.b.c.d", "site-op", SALT)).toBe(false);
  });

  it("an op-token signature is not accepted as a cookie (domain separation)", async () => {
    const token = await signOpToken("site-op", SALT);
    // Feed the op-token body in as if it were the cookie value.
    expect(await verifyCookie(`${COOKIE_NAME}=${token}`, "site-op", SALT)).toBe(false);
  });

  // Cross-pinned with lambdas/pkg/passcode/passcode_test.go
  // (crossPinned* constants). If either side's HMAC scheme drifts, exactly
  // one of these two suites goes red.
  it("accepts the Go-cross-pinned vector", async () => {
    const token = "site-pin.4102444800.0687580f2382fc3742256b57a28655ebbf478bfce3dcb1910e67ac78c84a96b4";
    expect(await verifyOpToken(token, "site-pin", "pin-salt-vector")).toBe(true);
  });
});

describe("operator bypass — /sites routing (iter 5.5)", () => {
  function envWithR2AndKV(kvValue: string | null) {
    return {
      PASSCODE_SALT: SALT,
      ENVIRONMENT: "test",
      PREVIEWS: {
        get: async (key: string) =>
          key === "sites/site-op/index.html"
            ? {
                body: "<!doctype html><title>preview</title>",
                httpEtag: '"abc"',
                writeHttpMetadata: (h: Headers) => h.set("content-type", "text/html"),
              }
            : null,
      } as unknown as R2Bucket,
      PREVIEW_PASSCODES_KV: {
        get: async () => kvValue,
      } as unknown as KVNamespace,
    };
  }

  it("valid ?op= serves the R2 document and sets the session cookie", async () => {
    resetRevocationCacheForTests();
    const env = envWithR2AndKV("some-hash");
    const token = await signOpToken("site-op", SALT);
    const res = await worker.fetch(
      new Request(`https://example.test/sites/site-op/?op=${encodeURIComponent(token)}`),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("preview");
    expect(res.headers.get("set-cookie")).toContain(`${COOKIE_NAME}=`);
    expect(res.headers.get("x-robots-tag")).toBe("noindex, nofollow");
  });

  it("invalid ?op= falls through to the passcode form", async () => {
    resetRevocationCacheForTests();
    const env = envWithR2AndKV("some-hash");
    const res = await worker.fetch(
      new Request("https://example.test/sites/site-op/?op=site-op.9999999999.bad"),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("This preview is private");
  });

  it("valid ?op= but revoked KV → revoked form, not content", async () => {
    resetRevocationCacheForTests();
    const env = envWithR2AndKV(null); // revoked
    const token = await signOpToken("site-op", SALT);
    const res = await worker.fetch(
      new Request(`https://example.test/sites/site-op/?op=${encodeURIComponent(token)}`),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("This preview has been revoked.");
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

  it("serves desktop/mobile screenshots (iter 5.5 sizes) with a valid cookie", async () => {
    resetRevocationCacheForTests();
    const env = {
      PASSCODE_SALT: SALT,
      ENVIRONMENT: "test",
      PREVIEWS: {
        get: async (key: string) =>
          key === "screenshots/site-shot/desktop.png"
            ? {
                body: "PNGDATA",
                httpEtag: '"p"',
                writeHttpMetadata: (_h: Headers) => {},
              }
            : null,
      } as unknown as R2Bucket,
      PREVIEW_PASSCODES_KV: { get: async () => "hash" } as unknown as KVNamespace,
    };
    const cookieHeader = (await signCookie("site-shot", SALT)).split(";")[0];
    const res = await worker.fetch(
      new Request("https://example.test/screenshots/site-shot/desktop.png", {
        headers: { cookie: cookieHeader },
      }),
      env,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
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
