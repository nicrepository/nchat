/**
 * The timeline's one ordering rule (issue #1082 review, finding 1): the
 * instant first, then the id — never the timestamp as text, which the read
 * cursor and the timeline once disagreed about.
 */

import { describe, expect, it } from "vitest";

import type { Message } from "../chatTypes";
import { unreadAfter } from "../readCursor";
import { compareTimelinePositions, insertMessageChronologically } from "./messageOrder";

const at = (createdAt: string, id: string) => ({ createdAt, id });

function message(id: string, createdAt: string): Message {
  return { id, createdAt, senderId: "them", status: "active" } as unknown as Message;
}

describe("compareTimelinePositions", () => {
  it("orders a whole second before a later fraction of it, which text gets backwards", () => {
    // As strings "…00Z" > "…00.1Z" ('Z' sorts after '.'); as instants it is first.
    expect("2026-07-15T10:00:00Z" > "2026-07-15T10:00:00.1Z").toBe(true);
    expect(
      compareTimelinePositions(at("2026-07-15T10:00:00Z", "b"), at("2026-07-15T10:00:00.1Z", "a")),
    ).toBeLessThan(0);
  });

  it("compares fractions of different lengths by value", () => {
    expect(
      compareTimelinePositions(
        at("2026-07-15T10:00:00.9Z", "a"),
        at("2026-07-15T10:00:00.123456Z", "b"),
      ),
    ).toBeGreaterThan(0);
    expect(
      compareTimelinePositions(
        at("2026-07-15T10:00:00.000001Z", "z"),
        at("2026-07-15T10:00:00.000002Z", "a"),
      ),
    ).toBeLessThan(0);
  });

  it("treats two notations of one instant as the same instant", () => {
    expect(
      compareTimelinePositions(
        at("2026-07-15T12:00:00.1Z", "x"),
        at("2026-07-15T09:00:00.100000000-03:00", "x"),
      ),
    ).toBe(0);
  });

  it("breaks a tie on the instant by id, as the server's (created_at, id) does", () => {
    const createdAt = "2026-07-15T10:00:00Z";
    expect(compareTimelinePositions(at(createdAt, "a"), at(createdAt, "b"))).toBeLessThan(0);
    // The same instant written another way is still a tie, decided by the id.
    expect(
      compareTimelinePositions(at("2026-07-15T10:00:00.000Z", "b"), at(createdAt, "a")),
    ).toBeGreaterThan(0);
  });

  it("puts the end of an instant after every message created at it", () => {
    const createdAt = "2026-07-15T10:00:00Z";
    expect(
      compareTimelinePositions({ createdAt, id: null }, at(createdAt, "zzzz")),
    ).toBeGreaterThan(0);
    expect(compareTimelinePositions({ createdAt, id: null }, { createdAt, id: null })).toBe(0);
  });
});

describe("insertMessageChronologically", () => {
  it("places a message by instant even when its text sorts the other way", () => {
    const whole = message("b", "2026-07-15T10:00:00Z");
    const fraction = message("a", "2026-07-15T10:00:00.1Z");

    const { messages, isNewer } = insertMessageChronologically([whole], fraction);

    expect(isNewer).toBe(true);
    expect(messages.map((m) => m.id)).toEqual(["b", "a"]);
  });

  it("re-sorts an out-of-order arrival into canonical order", () => {
    const drawn = [message("m1", "2026-07-15T10:00:00Z"), message("m3", "2026-07-15T10:00:01Z")];

    const { messages, isNewer } = insertMessageChronologically(
      drawn,
      message("m2", "2026-07-15T10:00:00.5Z"),
    );

    expect(isNewer).toBe(false);
    expect(messages.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
  });

  it("keeps the read cursor's count right on a timeline the two used to order differently", () => {
    // Text order would have put .1Z before 00Z and stopped the walk early.
    const drawn = [message("whole", "2026-07-15T10:00:00Z")];
    const { messages } = insertMessageChronologically(
      drawn,
      message("fraction", "2026-07-15T10:00:00.1Z"),
    );
    const later = insertMessageChronologically(
      messages,
      message("later", "2026-07-15T10:00:00.2Z"),
    ).messages;

    expect(unreadAfter(later, at("2026-07-15T10:00:00Z", "whole"), "me")).toBe(2);
  });
});
