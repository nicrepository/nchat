import { describe, expect, it } from "vitest";

import { isInternalPath, safeFrom } from "./safeRedirect";

describe("isInternalPath", () => {
  it("returns false for empty string", () => {
    expect(isInternalPath("")).toBe(false);
  });

  it("returns false for non-string values", () => {
    expect(isInternalPath(null)).toBe(false);
    expect(isInternalPath(undefined)).toBe(false);
    expect(isInternalPath(42)).toBe(false);
  });

  it("returns false when path does not start with /", () => {
    expect(isInternalPath("dashboard")).toBe(false);
    expect(isInternalPath("http://evil.com")).toBe(false);
    expect(isInternalPath("https://evil.com/path")).toBe(false);
  });

  it("returns false for protocol-relative URL starting with //", () => {
    expect(isInternalPath("//evil.com")).toBe(false);
    expect(isInternalPath("//")).toBe(false);
  });

  it("returns false for /\\ (Windows-style protocol-relative variant)", () => {
    expect(isInternalPath("/\\evil")).toBe(false);
  });

  it("returns true for /", () => {
    expect(isInternalPath("/")).toBe(true);
  });

  it("returns true for /dashboard", () => {
    expect(isInternalPath("/dashboard")).toBe(true);
  });

  it("returns true for nested internal paths", () => {
    expect(isInternalPath("/chat/room/123")).toBe(true);
  });
});

describe("safeFrom", () => {
  it("returns the path when it is a safe internal path", () => {
    expect(safeFrom("/dashboard")).toBe("/dashboard");
  });

  it("returns / when the value is undefined", () => {
    expect(safeFrom(undefined)).toBe("/");
  });

  it("returns / when the path is an external URL", () => {
    expect(safeFrom("https://evil.com")).toBe("/");
  });

  it("returns / when the path is protocol-relative //evil.com", () => {
    expect(safeFrom("//evil.com")).toBe("/");
  });

  it("returns / when the path is the /\\ variant", () => {
    expect(safeFrom("/\\evil")).toBe("/");
  });

  it("returns / when the value is null", () => {
    expect(safeFrom(null)).toBe("/");
  });
});
