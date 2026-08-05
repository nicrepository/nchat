import { describe, expect, it } from "vitest";

import { compareByActivity, laterActivity, parseInstant, sortByActivity } from "./sidebarOrder";
import type { ActivityOrdered } from "./sidebarOrder";

/**
 * ISSUE #414 — the sidebar's ordering rule, stated once and exercised here on
 * the shape all three sections share. Section independence is asserted in
 * ChatSidebar.test.tsx, where the three lists actually exist.
 */

function item(overrides: Partial<ActivityOrdered> & { id: string }): ActivityOrdered {
  return { name: overrides.id, ...overrides };
}

function ids(items: ActivityOrdered[]): string[] {
  return items.map(({ id }) => id);
}

describe("compareByActivity", () => {
  it("puts a conversation with messages before one without", () => {
    const active = item({ id: "a", lastMessageAt: "2020-01-01T00:00:00Z" });
    // Created far later than the other one was last written in: the empty
    // conversation must still lose, which `lastMessageAt ?? createdAt` would
    // have got backwards.
    const empty = item({ id: "b", createdAt: "2026-08-01T00:00:00Z" });

    expect(ids(sortByActivity([empty, active]))).toEqual(["a", "b"]);
  });

  it("orders active conversations by last message, newest first", () => {
    const older = item({ id: "older", lastMessageAt: "2026-07-30T09:00:00Z" });
    const newer = item({ id: "newer", lastMessageAt: "2026-07-30T10:00:00Z" });

    expect(ids(sortByActivity([older, newer]))).toEqual(["newer", "older"]);
  });

  it("compares instants, not strings, across offsets", () => {
    // 09:00-03:00 is 12:00Z — later than 11:00Z, though it sorts earlier as text.
    const withOffset = item({ id: "offset", lastMessageAt: "2026-07-30T09:00:00-03:00" });
    const utc = item({ id: "utc", lastMessageAt: "2026-07-30T11:00:00Z" });

    expect(ids(sortByActivity([utc, withOffset]))).toEqual(["offset", "utc"]);
  });

  it("orders two messages written inside the same second", () => {
    const earlier = item({ id: "earlier", lastMessageAt: "2026-08-04T12:00:00.100456Z" });
    const later = item({ id: "later", lastMessageAt: "2026-08-04T12:00:00.900123Z" });

    expect(ids(sortByActivity([earlier, later]))).toEqual(["later", "earlier"]);
  });

  // The case a millisecond-only comparison cannot see: both parse to the same
  // epoch, and only the digits past the millisecond tell them apart.
  it("orders two messages written inside the same millisecond", () => {
    const earlier = item({ id: "earlier", lastMessageAt: "2026-08-04T12:00:00.900045Z" });
    const later = item({ id: "later", lastMessageAt: "2026-08-04T12:00:00.900123Z" });

    expect(Date.parse("2026-08-04T12:00:00.900045Z")).toBe(
      Date.parse("2026-08-04T12:00:00.900123Z"),
    );
    expect(ids(sortByActivity([earlier, later]))).toEqual(["later", "earlier"]);
    expect(ids(sortByActivity([later, earlier]))).toEqual(["later", "earlier"]);
  });

  it("reads a fraction of any length, right-padded", () => {
    const items = [
      item({ id: "1-digit", lastMessageAt: "2026-08-04T12:00:00.1Z" }),
      item({ id: "3-digits", lastMessageAt: "2026-08-04T12:00:00.101Z" }),
      item({ id: "6-digits", lastMessageAt: "2026-08-04T12:00:00.100001Z" }),
      item({ id: "9-digits", lastMessageAt: "2026-08-04T12:00:00.100000001Z" }),
      item({ id: "no-fraction", lastMessageAt: "2026-08-04T12:00:00Z" }),
    ];

    expect(ids(sortByActivity(items))).toEqual([
      "3-digits", // .101
      "6-digits", // .100001
      "9-digits", // .100000001
      "1-digit", // .100000000
      "no-fraction", // .000000000
    ]);
  });

  it("treats equivalent fractions as the same instant, falling through to name and id", () => {
    // .1, .100 and .100000000 are one instant written three ways: RFC3339Nano
    // drops trailing zeros, so the same value reaches the client differently
    // depending on what it happened to be.
    const short = item({ id: "id-c", name: "beta", lastMessageAt: "2026-08-04T12:00:00.1Z" });
    const medium = item({ id: "id-b", name: "alfa", lastMessageAt: "2026-08-04T12:00:00.100Z" });
    const long = item({
      id: "id-a",
      name: "alfa",
      lastMessageAt: "2026-08-04T12:00:00.100000000Z",
    });

    expect(ids(sortByActivity([short, medium, long]))).toEqual(["id-a", "id-b", "id-c"]);
  });

  it("treats the same fractional instant written in two offsets as equal", () => {
    const utc = item({ id: "id-b", name: "same", lastMessageAt: "2026-08-04T12:00:00.900123Z" });
    const offset = item({
      id: "id-a",
      name: "same",
      lastMessageAt: "2026-08-04T09:00:00.900123-03:00",
    });

    expect(compareByActivity(utc, offset)).toBeGreaterThan(0);
    expect(ids(sortByActivity([utc, offset]))).toEqual(["id-a", "id-b"]);
  });

  it("orders conversations without messages by creation, newest first", () => {
    const older = item({ id: "older", createdAt: "2026-01-01T00:00:00Z" });
    const newer = item({ id: "newer", createdAt: "2026-06-01T00:00:00Z" });

    expect(ids(sortByActivity([older, newer]))).toEqual(["newer", "older"]);
  });

  // The fallback key gets the same precision as the primary one: two empty
  // conversations created in the same second, or the same millisecond, are
  // still two different creation instants.
  it("orders conversations without messages created inside the same second", () => {
    const older = item({ id: "older", createdAt: "2026-08-04T12:00:00.100456Z" });
    const newer = item({ id: "newer", createdAt: "2026-08-04T12:00:00.900123Z" });

    expect(ids(sortByActivity([older, newer]))).toEqual(["newer", "older"]);
  });

  it("orders conversations without messages created inside the same millisecond", () => {
    const older = item({ id: "older", createdAt: "2026-08-04T12:00:00.900045Z" });
    const newer = item({ id: "newer", createdAt: "2026-08-04T12:00:00.900123Z" });

    expect(ids(sortByActivity([older, newer]))).toEqual(["newer", "older"]);
    expect(ids(sortByActivity([newer, older]))).toEqual(["newer", "older"]);
  });

  it("breaks a timestamp tie by normalized name", () => {
    const sameInstant = "2026-07-30T10:00:00Z";
    const zeta = item({ id: "1", name: "  Zeta ", lastMessageAt: sameInstant });
    const alfa = item({ id: "2", name: "alfa", lastMessageAt: sameInstant });

    expect(ids(sortByActivity([zeta, alfa]))).toEqual(["2", "1"]);
  });

  it("breaks a full tie by id", () => {
    const sameInstant = "2026-07-30T10:00:00Z";
    const second = item({ id: "id-b", name: "Equipe", lastMessageAt: sameInstant });
    const first = item({ id: "id-a", name: "equipe", lastMessageAt: sameInstant });

    expect(ids(sortByActivity([second, first]))).toEqual(["id-a", "id-b"]);
    // Total order: no pair is ever "equal", so nothing depends on sort stability.
    expect(compareByActivity(first, second)).toBeLessThan(0);
    expect(compareByActivity(second, first)).toBeGreaterThan(0);
    expect(compareByActivity(first, first)).toBe(0);
  });

  it("treats an unparseable last message timestamp as no activity", () => {
    const broken = item({ id: "broken", lastMessageAt: "not-a-date" });
    const active = item({ id: "active", lastMessageAt: "2020-01-01T00:00:00Z" });

    expect(ids(sortByActivity([broken, active]))).toEqual(["active", "broken"]);
  });

  it("sinks an unparseable creation timestamp to the end of its group", () => {
    const broken = item({ id: "broken", createdAt: "" });
    const dated = item({ id: "dated", createdAt: "2020-01-01T00:00:00Z" });

    expect(ids(sortByActivity([broken, dated]))).toEqual(["dated", "broken"]);
    expect(ids(sortByActivity([dated, broken]))).toEqual(["dated", "broken"]);
  });

  it("produces the same order whatever the input order was", () => {
    const items = [
      item({ id: "c", name: "Canal C", lastMessageAt: "2026-07-30T10:00:00Z" }),
      item({ id: "a", name: "Canal A", createdAt: "2026-07-01T00:00:00Z" }),
      item({ id: "d", name: "Canal D", lastMessageAt: "2026-07-30T12:00:00Z" }),
      item({ id: "b", name: "Canal B", createdAt: "2026-07-15T00:00:00Z" }),
    ];
    const expected = ["d", "c", "b", "a"];

    expect(ids(sortByActivity(items))).toEqual(expected);
    expect(ids(sortByActivity([...items].reverse()))).toEqual(expected);
    // Reloading with the same persisted state yields the same order.
    expect(ids(sortByActivity(sortByActivity(items)))).toEqual(expected);
  });

  it("does not mutate its input", () => {
    const items = [
      item({ id: "b", lastMessageAt: "2026-07-01T00:00:00Z" }),
      item({ id: "a", lastMessageAt: "2026-07-02T00:00:00Z" }),
    ];
    const sorted = sortByActivity(items);

    expect(ids(items)).toEqual(["b", "a"]);
    expect(sorted).not.toBe(items);
  });
});

describe("laterActivity", () => {
  it("keeps the newer of the two instants", () => {
    expect(laterActivity("2026-07-30T10:00:00Z", "2026-07-30T12:00:00Z")).toBe(
      "2026-07-30T12:00:00Z",
    );
    expect(laterActivity("2026-07-30T12:00:00Z", "2026-07-30T10:00:00Z")).toBe(
      "2026-07-30T12:00:00Z",
    );
  });

  it("adopts an instant when there was none", () => {
    expect(laterActivity(null, "2026-07-30T10:00:00Z")).toBe("2026-07-30T10:00:00Z");
    expect(laterActivity(undefined, "2026-07-30T10:00:00Z")).toBe("2026-07-30T10:00:00Z");
  });

  it("keeps the current instant when the incoming one is absent or unusable", () => {
    expect(laterActivity("2026-07-30T10:00:00Z", null)).toBe("2026-07-30T10:00:00Z");
    expect(laterActivity("2026-07-30T10:00:00Z", "not-a-date")).toBe("2026-07-30T10:00:00Z");
    expect(laterActivity(null, null)).toBeNull();
  });

  it("is idempotent", () => {
    const once = laterActivity(null, "2026-07-30T10:00:00Z");
    expect(laterActivity(once, "2026-07-30T10:00:00Z")).toBe(once);
  });

  // The merge decides whether a WebSocket event is newer than what the sidebar
  // already holds, so it needs exactly the precision the ordering uses.
  it("keeps the newer instant when they differ below the millisecond", () => {
    expect(laterActivity("2026-08-04T12:00:00.900045Z", "2026-08-04T12:00:00.900123Z")).toBe(
      "2026-08-04T12:00:00.900123Z",
    );
    // And an event older by the same amount does not roll the sidebar back.
    expect(laterActivity("2026-08-04T12:00:00.900123Z", "2026-08-04T12:00:00.900045Z")).toBe(
      "2026-08-04T12:00:00.900123Z",
    );
  });

  it("reports no change when the same instant arrives written differently", () => {
    // Equal is not newer: an equivalent rewrite must not look like an update to
    // the caller that decides whether to allocate new state.
    for (const equivalent of [
      "2026-08-04T12:00:00.100Z",
      "2026-08-04T12:00:00.100000000Z",
      "2026-08-04T09:00:00.100-03:00",
    ]) {
      expect(laterActivity("2026-08-04T12:00:00.1Z", equivalent)).toBe("2026-08-04T12:00:00.1Z");
    }
  });
});

describe("parseInstant", () => {
  it("splits a timestamp into epoch milliseconds and the nanoseconds past it", () => {
    expect(parseInstant("2026-08-04T12:00:00.900123Z")).toEqual({
      epochMilliseconds: Date.parse("2026-08-04T12:00:00.900Z"),
      subMillisecondNanoseconds: 123000,
    });
  });

  it("right-pads the fraction to nine digits", () => {
    expect(parseInstant("2026-08-04T12:00:00.1Z")).toEqual(
      parseInstant("2026-08-04T12:00:00.100000000Z"),
    );
    expect(parseInstant("2026-08-04T12:00:00.100123456Z")?.subMillisecondNanoseconds).toBe(123456);
    expect(parseInstant("2026-08-04T12:00:00Z")?.subMillisecondNanoseconds).toBe(0);
  });

  it("resolves offsets before comparing", () => {
    expect(parseInstant("2026-08-04T09:00:00.900123-03:00")).toEqual(
      parseInstant("2026-08-04T12:00:00.900123Z"),
    );
    expect(parseInstant("2026-08-04T15:00:00.900123+03:00")).toEqual(
      parseInstant("2026-08-04T12:00:00.900123Z"),
    );
  });

  it("yields nothing for a value with no usable instant", () => {
    for (const value of ["not-a-date", "", null, undefined]) {
      expect(parseInstant(value)).toBeUndefined();
    }
  });
});
