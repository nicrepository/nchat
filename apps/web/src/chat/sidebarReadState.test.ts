import { describe, expect, it } from "vitest";

import type { ConversationReadState } from "./chatTypes";
import {
  acceptServerRead,
  acceptSidebarAnswer,
  applyArrival,
  applyMarkAllRequested,
  applyReadProgress,
  rememberRead,
  type ReadRow,
} from "./sidebarReadState";

const at = (minute: number) => `2026-07-28T12:${String(minute).padStart(2, "0")}:00Z`;
const p = (minute: number) => ({
  id: `m-${String(minute).padStart(2, "0")}`,
  createdAt: at(minute),
});

function state(unreadCount: number, point: number | null): ConversationReadState {
  return { unreadCount, readThrough: point === null ? null : p(point) };
}

/** A server answer: request started at `startedAt`, answer received at `receivedAt`. */
function answer<T extends ReadRow>(
  row: T,
  unread: number,
  point: number | null,
  startedAt: number,
  receivedAt = startedAt + 0.5,
  endsMarkAll = false,
): T {
  return acceptServerRead(row, state(unread, point), { startedAt, receivedAt }, { endsMarkAll });
}

const base = (unread: number, point: number | null, startedAt = 1) =>
  answer({ id: "c" } as ReadRow, unread, point, startedAt);

/** The open timeline reached m{through}. */
const read = <T extends ReadRow>(row: T, through: number) =>
  applyReadProgress(row, { readThrough: p(through) });

const unread = { eligible: true, counts: true, isMention: false };

describe("the count is the server's: reading moves the cursor, the answer moves the count", () => {
  it("takes nothing off the count for a read, and shows the write's answer", () => {
    let row = read(base(5, 0), 3);
    expect(row).toMatchObject({ unreadCount: 5, readThrough: p(3) });
    row = answer(row, 2, 3, 2);
    expect(row.unreadCount).toBe(2);
  });

  it("the heart of #1082: 5 → 3 → 1 → 0 as the server acknowledges each read", () => {
    let row = base(5, 0);
    row = answer(read(row, 2), 3, 2, 2);
    expect(row.unreadCount).toBe(3);
    row = answer(read(row, 4), 1, 4, 3);
    expect(row.unreadCount).toBe(1);
    row = answer(read(row, 5), 0, 5, 4);
    expect(row.unreadCount).toBe(0);
  });

  it("follows each write's answer while the reader is already further ahead", () => {
    let row = read(base(5, 0), 4);
    row = answer(row, 3, 2, 2);
    expect(row).toMatchObject({ unreadCount: 3, readThrough: p(4) });
    row = answer(row, 1, 4, 3);
    expect(row.unreadCount).toBe(1);
  });
});

describe("seventh review — a message held here is no proof the count included it", () => {
  it("m1 deleted without an event, a refetch says 4, m1..m3 read → 4 until the write says 2; never 1", () => {
    let row = base(5, 0);
    row = answer(row, 4, 0, 2);
    row = read(row, 3);
    expect(row.unreadCount).toBe(4);
    row = answer(row, 2, 3, 3);
    expect(row.unreadCount).toBe(2);
  });

  it("H3: base 3 (m3b, m4, m5), m3 read across a gap → 3 before and after the write", () => {
    let row = read(base(3, 2), 3);
    expect(row.unreadCount).toBe(3);
    row = answer(row, 3, 3, 2);
    expect(row.unreadCount).toBe(3);
  });

  it("H2: A=5, 3 read, m1 deleted, B=4 → 4, then the write's 2; never 1", () => {
    let row = read(base(5, 0), 3);
    expect(row.unreadCount).toBe(5);
    row = answer(row, 4, 0, 3);
    expect(row.unreadCount).toBe(4);
    row = answer(row, 2, 3, 4);
    expect(row.unreadCount).toBe(2);
  });
});

describe("deletions lower the count only as the server states it", () => {
  // [scenario, row before, answer after the deletion, expected count]
  const cases: [string, () => ReadRow, (row: ReadRow) => ReadRow, number][] = [
    // The sidebar is not told of deletions: the next answer brings them.
    [
      "A. delete seen in realtime by the timeline, then a refetch",
      () => base(3, 0),
      (row) => answer(row, 2, 0, 2),
      2,
    ],
    [
      "B. delete never delivered, reads made meanwhile",
      () => read(base(4, 0), 3),
      (row) => answer(row, 2, 3, 2),
      2,
    ],
    [
      "C. the reader's own message deleted: the count never had it",
      () => base(3, 0),
      (row) => answer(row, 3, 0, 2),
      3,
    ],
    ["D. the newest message deleted", () => base(2, 3), (row) => answer(row, 1, 3, 2), 1],
    [
      "E. deleted while a write was out: its answer is newer",
      () => read(base(4, 1), 3),
      (row) => answer(row, 1, 3, 2),
      1,
    ],
    ["F. deleted before a refetch started", () => base(5, 0), (row) => answer(row, 4, 0, 2), 4],
    [
      "G. deleted after a refetch started, before its count ran",
      () => base(5, 0),
      (row) => answer(row, 4, 0, 2, 3),
      4,
    ],
    [
      "H. a later refetch at the same read point, after a deletion",
      () => base(4, 2),
      (row) => answer(row, 3, 2, 5),
      3,
    ],
  ];

  it.each(cases)("%s", (_scenario, before, after, expected) => {
    const row = before();
    // Nothing the reader did lowered the server's count before it answered.
    expect(row.unreadCount).toBe(row.serverRead?.unreadCount);
    expect(after(row).unreadCount).toBe(expected);
  });
});

describe("H1 — the confirmed read frontier never moves back", () => {
  it("a refetch computed before a confirmed write does not undo it, and one refetch settles", () => {
    let row = applyMarkAllRequested(base(2, 3), 2);
    row = answer(row, 0, 5, 3, 5, true);
    expect(row).toMatchObject({ unreadCount: 0, confirmedAt: 5 });

    // Asked at 4, before the confirmation at 5, read before the commit: m3 / 2.
    row = answer(row, 2, 3, 4, 6);
    expect(row).toMatchObject({ unreadCount: 0, reconcileAfter: 6 });
    expect(row.serverRead?.readThrough).toEqual(p(5));

    row = answer(row, 0, 5, 7);
    expect(row).toMatchObject({ unreadCount: 0, reconcileAfter: undefined });
  });

  it("does not ask again for an answer asked after the confirmation and still behind", () => {
    let row = answer(base(2, 3), 0, 5, 2, 3);
    row = answer(row, 2, 3, 4);
    expect(row).toMatchObject({ unreadCount: 0, reconcileAfter: undefined });
  });

  it("takes an answer from further ahead — another device read further", () => {
    expect(answer(read(base(5, 0), 3), 0, 9, 5).unreadCount).toBe(0);
  });

  it("takes a newer request's count at the same point, up or down", () => {
    expect(answer(base(1, 4), 3, 4, 2).unreadCount).toBe(3);
    expect(answer(base(2, 3), 1, 3, 2).unreadCount).toBe(1);
  });
});

describe("reconciliation of upper bounds", () => {
  it("two concurrent answers at one point → the higher (2 → 4), and one refetch settles it (→ 2)", () => {
    let row = answer(base(2, 3), 4, 3, 1.2, 2);
    expect(row).toMatchObject({ unreadCount: 4, reconcileAfter: 2 });
    row = answer(row, 2, 3, 3);
    expect(row).toMatchObject({ unreadCount: 2, reconcileAfter: undefined });
  });

  it("keeps the higher base against a lower concurrent answer, and asks once", () => {
    let row = answer(base(3, 4), 1, 4, 1.2, 2);
    expect(row).toMatchObject({ unreadCount: 3, reconcileAfter: 2 });
    row = answer(row, 1, 4, 3);
    expect(row).toMatchObject({ unreadCount: 1, reconcileAfter: undefined });
  });

  it("an arrival seen while a refetch was out → counted on top (3 → 4), and one refetch settles it (→ 3)", () => {
    let row = applyArrival(base(2, 3), unread, p(6), 2.2);
    row = answer(row, 3, 3, 2, 2.5);
    expect(row).toMatchObject({ unreadCount: 4, reconcileAfter: 2.5 });
    row = answer(row, 3, 3, 3);
    expect(row).toMatchObject({ unreadCount: 3, reconcileAfter: undefined, arrivals: [] });
  });

  it("asks again only for something new while the reconciling refetch was out", () => {
    let row = answer(base(2, 3), 4, 3, 1.2, 2);
    row = applyArrival(row, unread, p(7), 3.2);
    row = answer(row, 3, 3, 3, 3.5);
    expect(row.reconcileAfter).toBe(3.5);
    row = answer(row, 3, 3, 4);
    expect(row.reconcileAfter).toBeUndefined();
  });
});

describe("arrivals", () => {
  it("adds an arrival past the base, once", () => {
    let row = applyArrival(base(2, 3), unread, p(6), 2);
    row = applyArrival(row, unread, p(6), 3);
    expect(row.unreadCount).toBe(3);
  });

  it("drops it under a base whose request started after it arrived", () => {
    let row = applyArrival(base(2, 3), unread, p(6), 2);
    row = answer(row, 3, 3, 3);
    expect(row).toMatchObject({ unreadCount: 3, arrivals: [] });
  });

  it("stops counting it once the reader's cursor passes it", () => {
    const openHere = { eligible: true, counts: false, isMention: false };
    let row = applyArrival(base(0, 5), openHere, p(6), 2);
    expect(row.unreadCount).toBe(1);
    row = read(row, 6);
    expect(row.unreadCount).toBe(0);
  });

  it("ignores the reader's own message", () => {
    const own = { eligible: false, counts: false, isMention: false };
    const row = base(2, 3);
    expect(applyArrival(row, own, p(6), 2)).toBe(row);
  });

  it("counts by the old rule against a server without the cursor", () => {
    expect(applyArrival({ id: "c", unreadCount: 1 }, unread, p(6), 2).unreadCount).toBe(2);
    const notCounted = { eligible: true, counts: false, isMention: false };
    expect(applyArrival({ id: "c", unreadCount: 1 }, notCounted, p(6), 2).unreadCount).toBe(1);
  });
});

describe("a server without the precise cursor", () => {
  const legacy = <T extends ReadRow>(row: T, unreadCount: number | undefined) =>
    acceptSidebarAnswer(row, { unreadCount, precise: false }, { startedAt: 9, receivedAt: 10 });

  it("replaces the precise state with its count — nothing precise is inherited", () => {
    const row = legacy(applyArrival(base(5, 0), unread, p(6), 2), 9);
    expect(row).toMatchObject({ unreadCount: 9, arrivals: undefined });
    expect(row.serverRead).toBeUndefined();
  });

  it("ignores read state a payload carries when it does not declare the cursor", () => {
    const row = acceptSidebarAnswer(
      base(5, 0),
      { readState: state(5, 0), unreadCount: 9, precise: false },
      { startedAt: 5, receivedAt: 6 },
    );
    expect(row.unreadCount).toBe(9);
    expect(row.serverRead).toBeUndefined();
  });

  it("keeps the live count when an older server omits it", () => {
    expect(legacy({ id: "c", unreadCount: 4 }, undefined).unreadCount).toBe(4);
  });

  it("adopts the precise server back from scratch", () => {
    const row = acceptSidebarAnswer(
      legacy(base(5, 0), 9),
      { readState: state(6, 3), unreadCount: 6, precise: true },
      { startedAt: 11, receivedAt: 12 },
    );
    expect(row).toMatchObject({ unreadCount: 6, serverRead: { readThrough: p(3) } });
  });

  it("keeps the server count when the legacy timeline may contain gaps", () => {
    const row = { id: "c", unreadCount: 5 } as ReadRow;
    expect(read(row, 3)).toBe(row);
  });
});

describe("mark-all without an invented cursor (R7)", () => {
  it("shows zero until answered, then the server's own point", () => {
    let row = applyMarkAllRequested(base(4, 1), 5);
    expect(row).toMatchObject({ unreadCount: 0, markAllSince: 5 });
    expect(row.readThrough).toBeUndefined();
    row = answer(row, 0, 5, 6, 7, true);
    expect(row).toMatchObject({ unreadCount: 0, markAllSince: undefined });
    expect(row.serverRead?.readThrough).toEqual(p(5));
  });

  it("keeps a mention arriving meanwhile at the same instant with a later id", () => {
    let row = applyMarkAllRequested(base(4, 1), 5);
    const late = { id: "m-05-late", createdAt: p(5).createdAt };
    row = applyArrival(row, { eligible: true, counts: true, isMention: true }, late, 7);
    expect(row).toMatchObject({ unreadCount: 1, hasMentionUnread: true });
    row = answer(row, 1, 5, 8, 9, true);
    expect(row).toMatchObject({ unreadCount: 1, hasMentionUnread: true, unreadMention: late });
  });

  it("ends the optimistic zero when the mark-all fails", () => {
    const row = acceptServerRead(applyMarkAllRequested(base(4, 1), 5), undefined, undefined, {
      endsMarkAll: true,
    });
    expect(row).toMatchObject({ unreadCount: 4, markAllSince: undefined });
  });

  it("zeroes the count against a server without the cursor", () => {
    expect(applyMarkAllRequested({ id: "c", unreadCount: 3 }, 1).unreadCount).toBe(0);
  });
});

describe("mentions — known and unknown are two facts", () => {
  const mention = { eligible: true, counts: false, isMention: true };

  it("restores a cached mention as unknown, never as no mention", () => {
    const row = rememberRead({ id: "c" }, undefined, { unreadCount: 3, hasMentionUnread: true });
    expect(row).toMatchObject({ unknownMention: true, hasMentionUnread: true, unreadCount: 3 });
  });

  it("keeps an unknown mention through reads and nonzero answers, and clears it on the server's zero", () => {
    let row = rememberRead({ id: "c" } as ReadRow, undefined, {
      unreadCount: 4,
      hasMentionUnread: true,
    });
    row = read(answer(row, 4, 0, 1), 3);
    expect(row).toMatchObject({ unreadCount: 4, unknownMention: true });
    row = answer(row, 2, 3, 2);
    expect(row).toMatchObject({ unreadCount: 2, unknownMention: true });
    row = answer(read(row, 5), 0, 5, 3);
    expect(row).toMatchObject({ unreadCount: 0, unknownMention: false, hasMentionUnread: false });
  });

  it("clears a known mention the cursor passes", () => {
    let row = applyArrival(base(0, 0), mention, p(2), 2);
    expect(row.hasMentionUnread).toBe(true);
    row = read(row, 3);
    expect(row.hasMentionUnread).toBe(false);
  });

  it("ignores a mention already read, locally or by the server", () => {
    const readLocally: ReadRow = { id: "c", readThrough: p(5) };
    expect(applyArrival(readLocally, mention, p(4), 2).hasMentionUnread).toBe(false);
    expect(applyArrival(base(0, 5), mention, p(4), 2).hasMentionUnread).toBe(false);
  });
});

describe("rows", () => {
  it("returns the very same row for a report that changes nothing", () => {
    const row = read(base(2, 3), 4);
    expect(read(row, 4)).toBe(row);
  });

  it("never moves the local cursor backwards", () => {
    expect(read(read(base(0, 0), 5), 2).readThrough).toEqual(p(5));
  });

  it("carries what a row knows across a refetch, before the answer is offered", () => {
    const before = applyArrival(read(base(5, 0), 3), unread, p(6), 2);
    const remembered = rememberRead({ id: "c", unreadCount: 9 }, before, undefined);
    expect(remembered).toMatchObject({
      unreadCount: 6,
      serverRead: before.serverRead,
      arrivals: before.arrivals,
      readThrough: p(3),
    });
  });
});
