import { describe, expect, it } from "vitest";

import {
  BYTES_PER_MIB,
  bytesToMiB,
  formatBytes,
  formatDateTime,
  isWholeMiB,
  mibToBytes,
} from "./units";

describe("isWholeMiB", () => {
  it("accepts exact whole MiB only", () => {
    expect(isWholeMiB(BYTES_PER_MIB)).toBe(true);
    expect(isWholeMiB(250 * BYTES_PER_MIB)).toBe(true);
    // 1.5 MiB cannot be shown in a field that edits whole MiB without being
    // changed, so it is not a value this form can edit.
    expect(isWholeMiB(BYTES_PER_MIB + BYTES_PER_MIB / 2)).toBe(false);
    expect(isWholeMiB(BYTES_PER_MIB + 1)).toBe(false);
    expect(isWholeMiB(0.5)).toBe(false);
  });
});

describe("mibToBytes", () => {
  it("converts exactly", () => {
    expect(mibToBytes(100)).toBe(104857600);
    expect(mibToBytes(0)).toBe(0);
  });

  // Returning null rather than a wrong number: a value past the safe integer
  // range would reach the server as a different limit than the one typed.
  it("refuses conversions it cannot represent exactly", () => {
    expect(mibToBytes(Number.MAX_SAFE_INTEGER)).toBeNull();
    expect(mibToBytes(1.5)).toBeNull();
    expect(mibToBytes(-1)).toBeNull();
    expect(mibToBytes(Number.NaN)).toBeNull();
    expect(mibToBytes(Number.POSITIVE_INFINITY)).toBeNull();
  });
});

describe("bytesToMiB", () => {
  it("reports whole MiB", () => {
    expect(bytesToMiB(104857600)).toBe(100);
    expect(bytesToMiB(BYTES_PER_MIB)).toBe(1);
  });
});

describe("formatBytes", () => {
  it("renders in the binary units the policy uses", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(250 * BYTES_PER_MIB)).toBe("250 MiB");
    expect(formatBytes(2 * 1024 * BYTES_PER_MIB)).toBe("2.0 GiB");
    expect(formatBytes(Number.NaN)).toBe("—");
  });
});

describe("formatDateTime", () => {
  it("renders an em dash rather than an invalid date", () => {
    expect(formatDateTime(null)).toBe("—");
    expect(formatDateTime("not a date")).toBe("—");
    expect(formatDateTime("2026-08-20T10:00:00Z")).not.toBe("—");
  });
});
