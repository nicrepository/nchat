import { describe, expect, it } from "vitest";

import { compareInstants, parseInstant } from "./instant";

describe("parseInstant", () => {
  it("has no instant for a missing value", () => {
    expect(parseInstant(null)).toBeUndefined();
    expect(parseInstant(undefined)).toBeUndefined();
  });

  it("has no instant for an empty or unparseable string", () => {
    expect(parseInstant("")).toBeUndefined();
    expect(parseInstant("not a date")).toBeUndefined();
  });

  it("refuses an RFC 3339 shape whose date does not exist", () => {
    expect(parseInstant("2026-13-01T12:00:00Z")).toBeUndefined();
    expect(parseInstant("2026-07-28T25:00:00.123456Z")).toBeUndefined();
  });

  it("falls back to Date.parse, at millisecond resolution, outside the RFC 3339 shape", () => {
    expect(parseInstant("Tue, 28 Jul 2026 12:00:00 GMT")).toEqual({
      epochMilliseconds: Date.UTC(2026, 6, 28, 12),
      subMillisecondNanoseconds: 0,
    });
  });

  it("keeps the digits past the millisecond, however the fraction is written", () => {
    expect(parseInstant("2026-07-28T12:00:00.123456789Z")).toEqual({
      epochMilliseconds: Date.UTC(2026, 6, 28, 12, 0, 0, 123),
      subMillisecondNanoseconds: 456789,
    });
    expect(parseInstant("2026-07-28T12:00:00.1Z")).toEqual(
      parseInstant("2026-07-28T12:00:00.100000000Z"),
    );
    expect(parseInstant("2026-07-28T12:00:00Z")?.subMillisecondNanoseconds).toBe(0);
  });

  it("reads the same moment under different offsets as one instant", () => {
    expect(parseInstant("2026-07-28T09:00:00.000078-03:00")).toEqual(
      parseInstant("2026-07-28T12:00:00.000078Z"),
    );
  });
});

describe("compareInstants", () => {
  const at = (value: string) => parseInstant(value)!;

  it("orders by millisecond first, then by what is below it", () => {
    expect(
      compareInstants(at("2026-07-28T12:00:00.001Z"), at("2026-07-28T12:00:00.002Z")),
    ).toBeLessThan(0);
    expect(
      compareInstants(at("2026-07-28T12:00:00.000078Z"), at("2026-07-28T12:00:00.000001Z")),
    ).toBeGreaterThan(0);
    expect(compareInstants(at("2026-07-28T12:00:00.5Z"), at("2026-07-28T12:00:00.500Z"))).toBe(0);
  });
});
