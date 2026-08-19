import { describe, expect, it } from "vitest";

import {
  displayNameLength,
  MAX_DISPLAY_NAME_CODE_POINTS,
  sanitizeDisplayName,
  validateDisplayName,
} from "./profileForm";

describe("sanitizeDisplayName", () => {
  it("leaves a normal name unchanged", () => {
    expect(sanitizeDisplayName("Ana Lima")).toBe("Ana Lima");
  });

  it("trims external whitespace", () => {
    expect(sanitizeDisplayName("  Ana Lima  ")).toBe("Ana Lima");
  });

  it("strips embedded control characters (NUL, LF, DEL)", () => {
    const withControlChars = "Ana\x00\nLima\x7f";
    expect(sanitizeDisplayName(withControlChars)).toBe("AnaLima");
  });
});

describe("validateDisplayName", () => {
  it("accepts a normal name", () => {
    expect(validateDisplayName("Ana Lima")).toBeNull();
  });

  it("rejects an empty value", () => {
    expect(validateDisplayName("")).toBe("Informe um nome de exibição.");
  });

  it("rejects a whitespace-only value", () => {
    expect(validateDisplayName("   ")).toBe("Informe um nome de exibição.");
  });

  it("trims before counting, so surrounding whitespace never tips the limit", () => {
    const exactly80 = "a".repeat(MAX_DISPLAY_NAME_CODE_POINTS);
    expect(validateDisplayName(`  ${exactly80}  `)).toBeNull();
  });

  it("accepts exactly the maximum length", () => {
    expect(validateDisplayName("a".repeat(MAX_DISPLAY_NAME_CODE_POINTS))).toBeNull();
  });

  it("rejects one character over the maximum length", () => {
    const message = validateDisplayName("a".repeat(MAX_DISPLAY_NAME_CODE_POINTS + 1));
    expect(message).toBe(`O nome deve ter no máximo ${MAX_DISPLAY_NAME_CODE_POINTS} caracteres.`);
  });

  it("counts by Unicode code point, not UTF-16 unit", () => {
    // Each of these emoji is one code point but two UTF-16 units.
    const eighty = "🙂".repeat(MAX_DISPLAY_NAME_CODE_POINTS);
    expect(displayNameLength(eighty)).toBe(MAX_DISPLAY_NAME_CODE_POINTS);
    expect(validateDisplayName(eighty)).toBeNull();
    expect(validateDisplayName(eighty + "🙂")).not.toBeNull();
  });

  // Finding 1 (Code Quality Review, ID 7): the server sanitizes with
  // sanitizeDisplayName (trim + strip control characters) before it counts,
  // so client-side validation has to sanitize the same way before counting —
  // otherwise a value the server would accept could be rejected here first.
  it("strips embedded control characters before validating", () => {
    const withControlChars = "Ana\x00\nLima\x7f";
    expect(validateDisplayName(withControlChars)).toBeNull();
  });

  it("accepts a value whose raw length is over 80 but whose sanitized length is not", () => {
    const visible = "a".repeat(MAX_DISPLAY_NAME_CODE_POINTS);
    const withControlChars = visible + "\x00".repeat(5);
    expect(displayNameLength(withControlChars)).toBeGreaterThan(MAX_DISPLAY_NAME_CODE_POINTS);
    expect(validateDisplayName(withControlChars)).toBeNull();
  });

  it("still rejects a value that is over the limit after sanitizing", () => {
    const tooLong = "a".repeat(MAX_DISPLAY_NAME_CODE_POINTS + 1) + "\x00".repeat(5);
    const message = validateDisplayName(tooLong);
    expect(message).toBe(`O nome deve ter no máximo ${MAX_DISPLAY_NAME_CODE_POINTS} caracteres.`);
  });
});
