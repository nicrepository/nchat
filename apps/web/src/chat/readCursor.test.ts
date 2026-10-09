import { describe, expect, it } from "vitest";

import {
  isReadThrough,
  laterReadPosition,
  openingReadCursor,
  pointInWindow,
  projectUnread,
  unreadAfter,
} from "./readCursor";

const ME = "me";
const THEM = "them";

function msg(n: number, senderId = THEM, status = "active") {
  return {
    id: `m${String(n).padStart(3, "0")}`,
    createdAt: `2026-07-15T10:00:${String(n % 60).padStart(2, "0")}.${String(n).padStart(6, "0")}Z`,
    senderId,
    status,
  };
}

const p = (n: number) => msg(n);

describe("laterReadPosition — the cursor never regresses", () => {
  it.each([
    [100, 101, 101],
    [101, 105, 105],
    [105, 103, 105],
    [105, 105, 105],
  ])("%i then %i keeps %i", (current, incoming, expected) => {
    expect(laterReadPosition(p(current), p(incoming)).id).toBe(p(expected).id);
  });

  it("keeps the current object itself when nothing advanced", () => {
    const current = p(105);
    expect(laterReadPosition(current, p(103))).toBe(current);
    expect(laterReadPosition(current, p(105))).toBe(current);
  });

  it("takes any position over no cursor at all", () => {
    expect(laterReadPosition(null, p(3)).id).toBe(p(3).id);
  });

  it("converges on the greatest whatever order positions arrive in", () => {
    const orders = [
      [120, 150, 130],
      [150, 120, 130],
      [130, 150, 120],
    ];
    for (const order of orders) {
      const cursor = order.reduce<ReturnType<typeof p> | null>(
        (current, n) => laterReadPosition(current, p(n)),
        null,
      );
      expect(cursor?.id).toBe(p(150).id);
    }
  });
});

describe("isReadThrough", () => {
  it("is true at and before the cursor, false after it and without one", () => {
    expect(isReadThrough(p(5), p(4))).toBe(true);
    expect(isReadThrough(p(5), p(5))).toBe(true);
    expect(isReadThrough(p(5), p(6))).toBe(false);
    expect(isReadThrough(undefined, p(1))).toBe(false);
  });
});

describe("unreadAfter", () => {
  const messages = [msg(1), msg(2), msg(3, ME), msg(4), msg(5, THEM, "removed"), msg(6)];

  it("counts eligible messages after the cursor only", () => {
    expect(unreadAfter(messages, p(1), ME)).toBe(3);
    expect(unreadAfter(messages, p(2), ME)).toBe(2);
    expect(unreadAfter(messages, p(4), ME)).toBe(1);
    expect(unreadAfter(messages, p(6), ME)).toBe(0);
  });

  it("never counts the reader's own messages, nor removed ones", () => {
    expect(unreadAfter([msg(1), msg(2, ME), msg(3, THEM, "removed")], p(1), ME)).toBe(0);
  });

  it("does not stop at an own message stamped earlier than the cursor", () => {
    // An optimistic send carries the browser's clock; it is not a position.
    const skewed = { ...msg(9, ME), createdAt: "2026-07-15T09:00:00Z" };
    expect(unreadAfter([msg(1), msg(2), skewed, msg(3)], p(1), ME)).toBe(2);
  });

  it("measures by position when the cursor message is not in the window", () => {
    expect(unreadAfter([msg(10), msg(11), msg(12)], p(10), ME)).toBe(2);
    // Same instant as m010, later id: m010 is behind it, m011 and m012 after.
    expect(unreadAfter([msg(10), msg(11), msg(12)], { ...p(10), id: "m010z" }, ME)).toBe(2);
  });
});

describe("unreadAfter from a server read point", () => {
  it("counts from the end of an instant when the point carries no message", () => {
    const messages = [msg(1), msg(2), msg(3)];
    // A legacy point at m2's instant: m2 and everything at that instant is read.
    expect(unreadAfter(messages, { createdAt: msg(2).createdAt, id: null }, ME)).toBe(1);
  });
});

describe("pointInWindow", () => {
  const messages = [msg(10), msg(11), msg(12)];

  it("accepts a point at or after the first loaded message", () => {
    expect(pointInWindow(p(10), messages)).toBe(true);
    expect(pointInWindow(p(12), messages)).toBe(true);
    expect(pointInWindow({ createdAt: msg(13).createdAt, id: null }, messages)).toBe(true);
  });

  it("refuses a point older than the window, no point, and an empty window", () => {
    expect(pointInWindow(p(9), messages)).toBe(false);
    expect(pointInWindow(null, messages)).toBe(false);
    expect(pointInWindow(undefined, messages)).toBe(false);
    expect(pointInWindow(p(10), [])).toBe(false);
  });
});

describe("openingReadCursor", () => {
  const messages = [msg(1), msg(2), msg(3), msg(4)];

  it("is the newest message when nothing was unread", () => {
    expect(openingReadCursor(messages, ME, 0)?.id).toBe(p(4).id);
  });

  it("is the message just before the first unread", () => {
    expect(openingReadCursor(messages, ME, 2)?.id).toBe(p(2).id);
  });

  it("is null when the boundary is older than the loaded window", () => {
    expect(openingReadCursor(messages, ME, 4)).toBeNull();
    expect(openingReadCursor(messages, ME, 9)).toBeNull();
    expect(openingReadCursor([], ME, 3)).toBeNull();
  });
});

describe("projectUnread", () => {
  const messages = [msg(1), msg(2), msg(3), msg(4), msg(5)];

  it("projects the remaining unread from the cursor once it is inside the window", () => {
    const base = { messages, currentUserId: ME, unreadCountAtOpen: 5, openTail: p(5) };
    expect(projectUnread({ ...base, cursor: p(2) })).toBe(3);
    expect(projectUnread({ ...base, cursor: p(4) })).toBe(1);
    expect(projectUnread({ ...base, cursor: p(5) })).toBe(0);
  });

  it("keeps the opening count, plus arrivals, while the cursor is before the window", () => {
    const withArrivals = [...messages, msg(6), msg(7, ME)];
    expect(
      projectUnread({
        messages: withArrivals,
        currentUserId: ME,
        cursor: null,
        unreadCountAtOpen: 30,
        openTail: p(5),
      }),
    ).toBe(31);
  });

  it("does not count history the reader scrolls back to", () => {
    // read cursor 1000, viewport at 700: the cursor is what counts.
    expect(
      projectUnread({
        messages,
        currentUserId: ME,
        cursor: p(5),
        unreadCountAtOpen: 0,
        openTail: p(5),
      }),
    ).toBe(0);
  });
});
