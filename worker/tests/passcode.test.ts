import { describe, it, expect } from "vitest";
import { hashPasscode, validatePasscode, signCookie, verifyCookie, COOKIE_NAME } from "../src/passcode";

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
