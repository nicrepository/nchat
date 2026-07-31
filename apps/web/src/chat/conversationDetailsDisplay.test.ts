import { describe, expect, it } from "vitest";

import { formatFileSize, formatLocalTime, isValidTimeZone } from "./conversationDetailsDisplay";

describe("formatFileSize", () => {
  it("keeps bytes whole below one kibibyte", () => {
    expect(formatFileSize(0)).toBe("0 B");
    expect(formatFileSize(1)).toBe("1 B");
    expect(formatFileSize(1023)).toBe("1023 B");
  });

  it("rolls over at each unit boundary instead of showing 1024 of the smaller one", () => {
    expect(formatFileSize(1024)).toBe("1,0 KB");
    expect(formatFileSize(1024 * 1024)).toBe("1,0 MB");
    expect(formatFileSize(1024 * 1024 * 1024)).toBe("1,0 GB");
  });

  it("drops the decimal above ten and uses the pt-BR separator", () => {
    expect(formatFileSize(2.4 * 1024 * 1024)).toBe("2,4 MB");
    expect(formatFileSize(128 * 1024 * 1024)).toBe("128 MB");
  });

  it("stops at the largest unit rather than dividing past it", () => {
    expect(formatFileSize(4096 * 1024 ** 4)).toBe("4096 TB");
  });

  it("returns nothing for a value that is not a size", () => {
    expect(formatFileSize(-1)).toBe("");
    expect(formatFileSize(Number.NaN)).toBe("");
    expect(formatFileSize(Number.POSITIVE_INFINITY)).toBe("");
  });
});

describe("isValidTimeZone", () => {
  it("accepts IANA zone names", () => {
    for (const zone of ["America/Sao_Paulo", "Europe/Lisbon", "Asia/Tokyo", "UTC"]) {
      expect(isValidTimeZone(zone)).toBe(true);
    }
  });

  it("rejects a fixed offset, which cannot describe daylight saving", () => {
    // ECMAScript accepts "-03:00" as an identifier, but a profile carrying one
    // would be frozen to whichever half of the year it was written in.
    expect(isValidTimeZone("-03:00")).toBe(false);
    expect(isValidTimeZone("+05:30")).toBe(false);
  });

  it("rejects anything that is not a zone at all", () => {
    for (const value of [
      "",
      "   ",
      "Nao/Existe",
      "<script>alert(1)</script>",
      "America/São_Paulo",
    ]) {
      expect(isValidTimeZone(value)).toBe(false);
    }
    expect(isValidTimeZone(undefined)).toBe(false);
  });
});

describe("formatLocalTime", () => {
  // One fixed instant, read from three places on Earth.
  const instant = new Date("2026-07-15T13:12:00.000Z");

  it("reads the clock in the given zone, not the runtime's", () => {
    expect(formatLocalTime(instant, "America/Sao_Paulo")).toBe("10:12");
    expect(formatLocalTime(instant, "Asia/Tokyo")).toBe("22:12");
    expect(formatLocalTime(instant, "UTC")).toBe("13:12");
  });

  it("follows daylight saving across the year", () => {
    // Lisbon: UTC+1 in July, UTC+0 in January. A stored offset gets one wrong.
    expect(formatLocalTime(instant, "Europe/Lisbon")).toBe("14:12");
    expect(formatLocalTime(new Date("2026-01-15T13:12:00.000Z"), "Europe/Lisbon")).toBe("13:12");
  });

  it("uses 24-hour time, so 00:xx never reads as 24:xx", () => {
    expect(formatLocalTime(new Date("2026-07-15T03:05:00.000Z"), "UTC")).toBe("03:05");
    expect(formatLocalTime(new Date("2026-07-15T00:05:00.000Z"), "UTC")).toBe("00:05");
    expect(formatLocalTime(new Date("2026-07-15T23:59:00.000Z"), "UTC")).toBe("23:59");
  });

  it("returns nothing when the zone is unusable", () => {
    expect(formatLocalTime(instant, undefined)).toBe("");
    expect(formatLocalTime(instant, "Nao/Existe")).toBe("");
    expect(formatLocalTime(instant, "-03:00")).toBe("");
  });

  it("returns nothing when the instant is not a date", () => {
    expect(formatLocalTime(new Date("nonsense"), "UTC")).toBe("");
  });
});
