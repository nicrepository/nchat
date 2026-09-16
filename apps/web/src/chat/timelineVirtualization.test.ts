/**
 * Timeline row model tests (issue #675).
 *
 * The rows are what the virtualizer indexes, so two properties matter beyond
 * "the dividers are in the right places": a row's key must survive a prepended
 * page unchanged — otherwise every cached measurement above the reader would
 * start describing a different row — and every row must be findable by key,
 * which is how a quote or a deep link reaches a message that is not mounted.
 */

import { describe, expect, it } from "vitest";

import {
  buildTimelineRows,
  MAX_SMOOTH_SCROLL_DISTANCE_PX,
  prependRestoreStep,
  scrollToEndBehavior,
  shouldShiftReadingPositionForResize,
  shouldVirtualize,
  timelineRowIndex,
  UNREAD_DIVIDER_KEY,
  VIRTUALIZE_MIN_ROWS,
  type TimelineRow,
} from "./timelineVirtualization";
import type { Message } from "./chatTypes";

function message(overrides: Partial<Message> = {}): Message {
  return {
    id: "m1",
    senderId: "u1",
    senderName: "Ana",
    bodyText: "oi",
    bodyFormat: "v2",
    createdAt: "2026-07-15T12:00:00.000Z",
    updatedAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  } as Message;
}

/** Day = the date part, minute = the whole timestamp up to the minute. */
const day = (iso: string) => iso.slice(0, 10);
const minute = (iso: string) => iso.slice(0, 16);

function rows(messages: Message[], firstUnreadMessageId: string | null = null): TimelineRow[] {
  return buildTimelineRows(messages, firstUnreadMessageId, day, minute);
}

describe("buildTimelineRows", () => {
  it("opens each day with its divider and nothing else", () => {
    const built = rows([
      message({ id: "a", createdAt: "2026-07-14T09:00:00.000Z" }),
      message({ id: "b", createdAt: "2026-07-15T09:00:00.000Z" }),
      message({ id: "c", createdAt: "2026-07-15T10:00:00.000Z" }),
    ]);

    expect(built.map((row) => row.key)).toEqual([
      "day:2026-07-14",
      "a",
      "day:2026-07-15",
      "b",
      "c",
    ]);
  });

  it("places the unread separator immediately before the first unread message", () => {
    const built = rows([message({ id: "a" }), message({ id: "b" }), message({ id: "c" })], "b");

    const keys = built.map((row) => row.key);
    expect(keys.indexOf(UNREAD_DIVIDER_KEY)).toBe(keys.indexOf("b") - 1);
  });

  it("groups a message that follows the same sender within the same minute", () => {
    const built = rows([
      message({ id: "a", senderId: "u1", createdAt: "2026-07-15T12:00:10.000Z" }),
      message({ id: "b", senderId: "u1", createdAt: "2026-07-15T12:00:40.000Z" }),
      message({ id: "c", senderId: "u2", createdAt: "2026-07-15T12:00:50.000Z" }),
    ]);

    const grouped = built
      .filter((row): row is Extract<TimelineRow, { type: "msg" }> => row.type === "msg")
      .map((row) => row.isGrouped);
    expect(grouped).toEqual([false, true, false]);
  });

  it("never groups across a day divider", () => {
    const built = rows([
      message({ id: "a", senderId: "u1", createdAt: "2026-07-14T12:00:00.000Z" }),
      message({ id: "b", senderId: "u1", createdAt: "2026-07-15T12:00:00.000Z" }),
    ]);

    const second = built.at(-1) as Extract<TimelineRow, { type: "msg" }>;
    expect(second.isGrouped).toBe(false);
  });
});

describe("row identity across a prepended page", () => {
  it("keeps every existing key unchanged when older messages are loaded above", () => {
    const current = [
      message({ id: "b", createdAt: "2026-07-15T09:00:00.000Z" }),
      message({ id: "c", createdAt: "2026-07-15T10:00:00.000Z" }),
    ];
    const before = rows(current).map((row) => row.key);

    const older = message({ id: "a", createdAt: "2026-07-14T09:00:00.000Z" });
    const after = rows([older, ...current]).map((row) => row.key);

    // The tail of the new list is the old list, key for key — which is what
    // lets the virtualizer keep the heights it already measured, and what keeps
    // the reader's position from drifting on every page of history.
    expect(after.slice(after.length - before.length)).toEqual(before);
  });

  it("shifts every index by the number of rows added, which keys do not follow", () => {
    const current = [message({ id: "b" }), message({ id: "c" })];
    const older = message({ id: "a", createdAt: "2026-07-14T09:00:00.000Z" });

    const beforeIndex = timelineRowIndex(rows(current));
    const afterIndex = timelineRowIndex(rows([older, ...current]));

    expect(beforeIndex.get("c")).toBe(2);
    expect(afterIndex.get("c")).toBe(4);
  });
});

describe("timelineRowIndex", () => {
  it("finds a message that is loaded but far from the mounted window", () => {
    const many = Array.from({ length: 200 }, (_, i) =>
      message({ id: `m${i}`, createdAt: "2026-07-15T12:00:00.000Z" }),
    );

    const index = timelineRowIndex(rows(many));

    expect(index.get("m180")).toBe(181); // one day divider ahead of it
    expect(index.has("not-loaded")).toBe(false);
  });
});

describe("shouldVirtualize", () => {
  it("leaves a single page of history rendered in full", () => {
    expect(shouldVirtualize(VIRTUALIZE_MIN_ROWS - 1)).toBe(false);
  });

  it("virtualizes once the loaded window justifies it", () => {
    expect(shouldVirtualize(VIRTUALIZE_MIN_ROWS)).toBe(true);
  });
});

describe("shouldShiftReadingPositionForResize", () => {
  // The reader is 1000px down the content; a row is described by where it
  // starts and the size the model still holds for it.
  const reading = 1000;
  const resize = (over: Partial<Parameters<typeof shouldShiftReadingPositionForResize>[0]>) =>
    shouldShiftReadingPositionForResize({
      rowStartPx: 0,
      rowSizePx: 100,
      readingOffsetPx: reading,
      isFirstMeasurement: false,
      restoring: false,
      scrollingToEnd: false,
      ...over,
    });

  describe("while something else owns the scroll position", () => {
    it("never adjusts during a prepend restoration, whatever the row is", () => {
      // The restoration measures the anchor's real box and corrects to it. An
      // adjustment landing in the same cycle is a second writer on the same
      // value — the single-authority rule this exists to hold.
      expect(resize({ restoring: true, rowStartPx: 0, rowSizePx: 100 })).toBe(false);
      expect(resize({ restoring: true, isFirstMeasurement: true, rowStartPx: 0 })).toBe(false);
      expect(resize({ restoring: true, rowStartPx: 900, rowSizePx: 400 })).toBe(false);
    });

    it("never adjusts during an explicit scroll to the end", () => {
      // The browser is animating that scroll, and any programmatic write to
      // scrollTop cancels it — leaving the reader stranded wherever the write
      // landed, with nothing left to finish the trip.
      expect(resize({ scrollingToEnd: true, rowStartPx: 0, rowSizePx: 100 })).toBe(false);
      expect(resize({ scrollingToEnd: true, isFirstMeasurement: true, rowStartPx: 0 })).toBe(false);
    });
  });

  describe("a first measurement", () => {
    it("adjusts for a row that starts above the reader", () => {
      // The estimate it replaces described a block already scrolled past, so
      // the whole difference belongs above them — including for the prepended
      // page's own first row, which crosses the edge.
      expect(resize({ isFirstMeasurement: true, rowStartPx: 900, rowSizePx: 400 })).toBe(true);
      expect(resize({ isFirstMeasurement: true, rowStartPx: 200, rowSizePx: 300 })).toBe(true);
    });

    it("does not adjust for a row starting at or below the reader", () => {
      expect(resize({ isFirstMeasurement: true, rowStartPx: reading, rowSizePx: 300 })).toBe(false);
      expect(resize({ isFirstMeasurement: true, rowStartPx: 1200, rowSizePx: 300 })).toBe(false);
    });
  });

  describe("a re-measurement", () => {
    it("adjusts for a row entirely above the reader", () => {
      expect(resize({ rowStartPx: 200, rowSizePx: 300 })).toBe(true);
    });

    it("adjusts for a row ending exactly at the reading position", () => {
      expect(resize({ rowStartPx: 700, rowSizePx: 300 })).toBe(true);
    });

    it("does not adjust for a row that crosses the reading position", () => {
      // The case the "starts above" rule got wrong. The reader is looking at
      // this row; an attachment in it finishing its layout grows it downward,
      // below their eyes. Shifting by that delta would yank them 200px down
      // because content they cannot see got taller.
      expect(resize({ rowStartPx: 900, rowSizePx: 400 })).toBe(false);
    });

    it("does not adjust for a row entirely below the reader", () => {
      expect(resize({ rowStartPx: 1200, rowSizePx: 300 })).toBe(false);
    });
  });

  it("is independent of how the reader got there", () => {
    // The whole point: the virtualizer's own default refuses to compensate
    // while scrolling backward, and reaching the top sentinel — the only way to
    // trigger a prepend — is scrolling backward.
    expect(resize({ isFirstMeasurement: true, rowStartPx: 0, rowSizePx: 92 })).toBe(true);
  });
});

describe("scrollToEndBehavior", () => {
  // O contrato, dito em uma linha: até o limite, inclusive, anima; passou dele,
  // chega de uma vez. E movimento reduzido vence sempre.
  const limit = MAX_SMOOTH_SCROLL_DISTANCE_PX;

  it("animates a trip well inside the limit", () => {
    expect(scrollToEndBehavior(1000, false)).toBe("smooth");
  });

  it("animates a trip of exactly the limit", () => {
    // A fronteira pertence ao lado animado — o teste existe para que ela pare
    // de ser uma pergunta.
    expect(scrollToEndBehavior(limit, false)).toBe("smooth");
  });

  it("arrives at once one pixel past the limit", () => {
    expect(scrollToEndBehavior(limit + 1, false)).toBe("auto");
  });

  it("arrives at once across a whole conversation", () => {
    // O caso real: uma animação de trinta mil pixels é longa o bastante para
    // uma escrita concorrente em scrollTop cancelá-la, e uma animação cancelada
    // não tem quem a termine — o leitor fica encalhado onde a escrita caiu.
    expect(scrollToEndBehavior(35_000, false)).toBe("auto");
  });

  it("lets reduced motion win, however short the trip", () => {
    expect(scrollToEndBehavior(10, true)).toBe("auto");
    expect(scrollToEndBehavior(limit, true)).toBe("auto");
    expect(scrollToEndBehavior(0, true)).toBe("auto");
  });
});

describe("prependRestoreStep", () => {
  /**
   * O leitor tinha a âncora 120px abaixo da borda, o modelo diz que ela está em
   * 5000, e a viewport está em 4000. As variações abaixo mexem numa coisa de
   * cada vez.
   */
  const step = (over: Partial<Parameters<typeof prependRestoreStep>[0]> = {}) =>
    prependRestoreStep({
      anchorIndex: 42,
      anchorOffsetPx: 120,
      measuredOffsetPx: null,
      modelScrollTopPx: 5000,
      currentScrollTopPx: 4000,
      hasBeenMeasured: false,
      ...over,
    });

  describe("terminações sem movimento", () => {
    it("finishes when the anchor is no longer in the row model", () => {
      // Uma mensagem apagada, ou a conversa recarregada: não há posição para
      // restaurar, e segurar a autoridade só manteria a compensação do
      // virtualizer desligada.
      expect(step({ anchorIndex: undefined })).toEqual({ kind: "finish" });
    });

    it("finishes when the anchor is gone even if it had already been measured", () => {
      expect(
        step({ anchorIndex: undefined, hasBeenMeasured: true, measuredOffsetPx: 120 }),
      ).toEqual({ kind: "finish" });
    });

    it("finishes when the scrollport is already where the model points and the row still is not mounted", () => {
      // A escrita não produziria movimento, logo não haveria commit seguinte:
      // esperar por um seria esperar para sempre.
      expect(step({ modelScrollTopPx: 4000, currentScrollTopPx: 4000 })).toEqual({
        kind: "finish",
      });
    });

    it("finishes when the row leaves the window after having been measured", () => {
      // O alvo do modelo é a pior das duas respostas depois de haver medição, e
      // reemiti-lo desfaria a correção medida.
      expect(step({ hasBeenMeasured: true })).toEqual({ kind: "finish" });
    });

    it("finishes when the anchor is already within tolerance", () => {
      expect(step({ measuredOffsetPx: 121, hasBeenMeasured: true })).toEqual({ kind: "finish" });
      expect(step({ measuredOffsetPx: 119, hasBeenMeasured: true })).toEqual({ kind: "finish" });
    });
  });

  describe("passos com movimento", () => {
    it("seeks the model position while the row has never been mounted", () => {
      expect(step()).toEqual({ kind: "seek", scrollTopPx: 5000 });
    });

    it("corrects by the measured difference, in either direction", () => {
      expect(step({ measuredOffsetPx: 300, hasBeenMeasured: true })).toEqual({
        kind: "correct",
        deltaPx: 180,
      });
      expect(step({ measuredOffsetPx: 20, hasBeenMeasured: true })).toEqual({
        kind: "correct",
        deltaPx: -100,
      });
    });

    it("corrects rather than finishing just past the tolerance", () => {
      expect(step({ measuredOffsetPx: 122, hasBeenMeasured: true })).toEqual({
        kind: "correct",
        deltaPx: 2,
      });
    });
  });
});
