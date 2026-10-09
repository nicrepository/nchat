import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConversationReadState } from "./chatApi";
import {
  createReadCursorWriter,
  READ_CURSOR_DEBOUNCE_MS,
  readTargetFromKey,
  readTargetKey,
  type ReadTarget,
} from "./readCursorWriter";

const A: ReadTarget = { kind: "channel", targetId: "a" };
const B: ReadTarget = { kind: "dm", targetId: "b" };
const at = (n: number) => ({
  id: `m${n}`,
  createdAt: `2026-07-15T10:00:${String(n).padStart(2, "0")}Z`,
});

interface Call {
  target: ReadTarget;
  id: string | undefined;
  keepalive: boolean;
  resolve: (state?: ConversationReadState) => void;
  reject: (reason: unknown) => void;
}

function controlledSend() {
  const calls: Call[] = [];
  const send = vi.fn(
    (target: ReadTarget, id: string | undefined, options: { keepalive: boolean }) =>
      new Promise<ConversationReadState | undefined>((resolve, reject) => {
        calls.push({ target, id, keepalive: options.keepalive, resolve, reject });
      }),
  );
  return { send, calls };
}

function writerWith(send: ReturnType<typeof controlledSend>["send"]) {
  const onSettled = vi.fn();
  return { writer: createReadCursorWriter(send, onSettled), onSettled };
}

async function elapse(ms = READ_CURSOR_DEBOUNCE_MS) {
  await vi.advanceTimersByTimeAsync(ms);
}

const ids = (calls: Call[]) => calls.map((call) => call.id);

/** The server's read state after a write that read through m{n}. */
const answer = (n: number, unreadCount = 0): ConversationReadState => ({
  unreadCount,
  readThrough: at(n),
});

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("createReadCursorWriter — the explicit cursor", () => {
  it("coalesces a burst into one write of the greatest position", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);

    for (const n of [1, 2, 3, 6]) writer.advance(A, at(n));
    expect(send).not.toHaveBeenCalled();
    await elapse();

    expect(ids(calls)).toEqual(["m6"]);
  });

  it("drops duplicates and positions the server already holds", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(5));
    await elapse();
    calls[0].resolve(answer(5, 0));
    await elapse(0);

    writer.advance(A, at(5));
    writer.advance(A, at(3));
    await elapse();

    expect(send).toHaveBeenCalledTimes(1);
  });

  it("keeps one write in flight and sends the greatest pending one when it settles", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(4));
    writer.advance(A, at(6));
    await elapse();
    expect(send).toHaveBeenCalledTimes(1);

    calls[0].resolve(answer(3, 2));
    await elapse(0);
    // One window after the write settles, never at once.
    expect(ids(calls)).toEqual(["m3"]);
    await elapse(READ_CURSOR_DEBOUNCE_MS - 1);
    expect(ids(calls)).toEqual(["m3"]);
    await elapse(1);
    expect(ids(calls)).toEqual(["m3", "m6"]);
  });

  it("does not send a pending position the answer shows the server already passed", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(5));
    // Another device read further meanwhile: the answer is already past 5.
    calls[0].resolve(answer(8, 0));
    await elapse();

    expect(send).toHaveBeenCalledTimes(1);
  });

  it("lets a later advance go out after a failed write, without retrying the failure", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(new Error("offline"));
    await elapse();
    expect(send).toHaveBeenCalledTimes(1);

    writer.advance(A, at(4));
    await elapse();

    expect(ids(calls)).toEqual(["m3", "m4"]);
  });

  it("survives a send that throws synchronously", async () => {
    const send = vi.fn(() => {
      throw new Error("boom");
    });
    const writer = createReadCursorWriter(send, vi.fn());
    writer.advance(A, at(1));
    await elapse();
    writer.advance(A, at(2));
    await elapse();

    expect(send).toHaveBeenCalledTimes(2);
  });

  it("keeps every conversation's writes to its own target", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(7));
    writer.advance(B, at(2));
    await elapse();

    expect(calls.map(({ target, id }) => [readTargetKey(target), id])).toEqual([
      ["channel:a", "m7"],
      ["dm:b", "m2"],
    ]);
  });

  it("hands every outcome to its owner, failures included", async () => {
    const { send, calls } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(6));
    calls[0].resolve(answer(3, 2));
    await elapse(0);
    expect(onSettled).toHaveBeenLastCalledWith(A, {
      request: at(3),
      ok: true,
      state: answer(3, 2),
    });

    await elapse();
    calls[1].reject(new Error("offline"));
    await elapse(0);
    expect(onSettled).toHaveBeenLastCalledWith(A, { request: at(6), ok: false });
  });

  // R8 (#1082 review): acknowledgement only moves forward, whichever request
  // brought it.
  it("ignores an answer older than one already acknowledged", async () => {
    const { send, calls } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(9));
    // The page is hidden away: m9 goes out beside the m3 still in flight.
    writer.flushOnUnload();
    await Promise.resolve();
    expect(ids(calls)).toEqual(["m3", "m9"]);

    // The terminal m9 answers first (the page lived on), then the old m3.
    calls[1].resolve(answer(9, 0));
    await elapse(0);
    calls[0].resolve(answer(3, 6));
    await elapse(0);

    expect(onSettled).toHaveBeenNthCalledWith(1, A, {
      request: at(9),
      ok: true,
      state: answer(9, 0),
    });
    // The late, lower answer carries nothing: no state for the owner to take.
    expect(onSettled).toHaveBeenNthCalledWith(2, A, { request: at(3), ok: true, state: undefined });
  });
});

describe("createReadCursorWriter — mark the whole conversation read", () => {
  it("keeps a cursor observed while the mark-all is in flight, and sends it after", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.markAll(A);
    await elapse();
    expect(ids(calls)).toEqual([undefined]);

    // m9 arrives and is read while the mark-all is on the wire.
    writer.advance(A, at(9));
    // The server resolved "everything" to m8, the newest it held then.
    calls[0].resolve(answer(8, 1));
    await elapse();

    expect(ids(calls)).toEqual([undefined, "m9"]);
  });

  it("sends only the mark-all when nothing new is read", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.markAll(A);
    await elapse();
    calls[0].resolve(answer(8, 0));
    await elapse();

    expect(ids(calls)).toEqual([undefined]);
  });

  it("does not send a cursor the mark-all already covered", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.markAll(A);
    await elapse();
    writer.advance(A, at(8));
    writer.advance(A, at(7));
    calls[0].resolve(answer(8, 0));
    await elapse();

    expect(ids(calls)).toEqual([undefined]);
  });

  it("lets a later mark-all go out again", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.markAll(A);
    await elapse();
    calls[0].resolve(answer(8, 0));
    await elapse(0);
    writer.markAll(A);
    await elapse();

    expect(ids(calls)).toEqual([undefined, undefined]);
  });
});

describe("createReadCursorWriter — the session it belongs to", () => {
  it("does not schedule more work when an in-flight request settles after disposal", async () => {
    const { send, calls } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(1));
    await elapse();
    writer.advance(A, at(2));
    writer.dispose();
    calls[0].resolve(answer(1, 1));
    await elapse(0);
    expect(onSettled).not.toHaveBeenCalled();
    expect(send).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not start a flushed request if the session ends before its microtask runs", async () => {
    const { send } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(3));
    writer.flush();
    writer.dispose();
    await elapse(0);

    expect(send).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();
  });

  it("drops a debounced write when the session ends before it goes out", async () => {
    const { send } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(4));
    writer.dispose();
    await elapse();

    expect(send).not.toHaveBeenCalled();
  });

  it("ignores the late answer of a write from an ended session, and sends nothing after it", async () => {
    const { send, calls } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(4));
    await elapse();
    writer.advance(A, at(5));
    writer.dispose();

    calls[0].resolve(answer(4, 0));
    await elapse();

    expect(onSettled).not.toHaveBeenCalled();
    expect(send).toHaveBeenCalledTimes(1);
    writer.advance(A, at(6));
    await elapse();
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("leaves the next session's writer free to write", async () => {
    const { send, calls } = controlledSend();
    const first = writerWith(send).writer;
    first.advance(A, at(4));
    first.dispose();

    const second = writerWith(send).writer;
    second.advance(A, at(2));
    await elapse();

    expect(ids(calls)).toEqual(["m2"]);
  });
});

describe("createReadCursorWriter — the page going away", () => {
  it("sends the pending cursor at once, as keepalive", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    writer.advance(A, at(4));

    writer.flushOnUnload();
    // Microtasks only: no timer is allowed to stand between the page and it.
    await Promise.resolve();

    expect(calls.map(({ id, keepalive }) => [id, keepalive])).toEqual([["m4", true]]);
  });

  it("does not lose a greater pending cursor behind a write already in flight", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(6));

    writer.flushOnUnload();
    await Promise.resolve();

    // The terminal exception to "one in flight": 6 goes out beside 3, and the
    // server keeps the greater whichever lands last.
    expect(calls.map(({ id, keepalive }) => [id, keepalive])).toEqual([
      ["m3", false],
      ["m6", true],
    ]);
  });

  it("sends nothing when nothing is pending", async () => {
    const { send } = controlledSend();
    const { writer } = writerWith(send);
    writer.flushOnUnload();
    await elapse();

    expect(send).not.toHaveBeenCalled();
  });

  it("sends a debounced write early as keepalive when the tab is hidden, still one in flight", async () => {
    const { send, calls } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    writer.flush();
    await Promise.resolve();
    writer.advance(A, at(5));
    writer.flush();
    await Promise.resolve();

    expect(calls.map(({ id, keepalive }) => [id, keepalive])).toEqual([["m3", true]]);
  });

  it("is not what ending a session does: a disposed writer sends nothing on unload", async () => {
    const { send } = controlledSend();
    const { writer } = writerWith(send);
    writer.advance(A, at(3));
    writer.dispose();
    writer.flushOnUnload();
    await elapse();

    expect(send).not.toHaveBeenCalled();
  });
});

describe("createReadCursorWriter — a page restored from the back/forward cache", () => {
  // R8 (#1082 review): pagehide with `persisted` may come back. The writer is
  // not ended by it: it keeps acknowledging and writing as before.
  it("keeps writing normally after the page-hide flush", async () => {
    const { send, calls } = controlledSend();
    const { writer, onSettled } = writerWith(send);
    writer.advance(A, at(3));
    writer.flushOnUnload();
    await Promise.resolve();
    calls[0].resolve(answer(3, 2));
    await elapse(0);
    expect(onSettled).toHaveBeenLastCalledWith(A, {
      request: at(3),
      ok: true,
      state: answer(3, 2),
    });

    // pageshow: the reader goes on reading.
    writer.advance(A, at(5));
    await elapse();
    expect(calls.map(({ id, keepalive }) => [id, keepalive])).toEqual([
      ["m3", true],
      ["m5", false],
    ]);
  });
});

describe("readTargetFromKey", () => {
  it("inverts readTargetKey and refuses anything else", () => {
    expect(readTargetFromKey(readTargetKey(A))).toEqual(A);
    expect(readTargetFromKey("dm:x:y")).toEqual({ kind: "dm", targetId: "x:y" });
    expect(readTargetFromKey("")).toBeUndefined();
    expect(readTargetFromKey("channel:")).toBeUndefined();
    expect(readTargetFromKey("group:x")).toBeUndefined();
  });
});

// #1082 fourth review (HIGH 5 / K): whether the server takes positions can
// change while a write waits; it is asked when the write is sent.
describe("createReadCursorWriter — a server that stops taking positions", () => {
  function writerAccepting(send: ReturnType<typeof controlledSend>["send"]) {
    const capability = { current: true };
    const writer = createReadCursorWriter(send, vi.fn(), {
      acceptsPositions: () => capability.current,
    });
    return { writer, capability };
  }

  it("drops a queued position when the capability goes away during the debounce", async () => {
    const { send } = controlledSend();
    const { writer, capability } = writerAccepting(send);
    writer.advance(A, at(3));
    capability.current = false;
    await elapse();

    expect(send).not.toHaveBeenCalled();
  });

  it("drops a position held behind a write in flight, but still sends a mark-all", async () => {
    const { send, calls } = controlledSend();
    const { writer, capability } = writerAccepting(send);
    writer.markAll(A);
    await elapse();
    writer.advance(A, at(4));
    capability.current = false;
    calls[0].resolve(answer(2, 1));
    await elapse();

    expect(ids(calls)).toEqual([undefined]);
  });

  it("sends nothing positional as the page goes away", async () => {
    const { send, calls } = controlledSend();
    const { writer, capability } = writerAccepting(send);
    writer.advance(A, at(3));
    writer.markAll(B);
    capability.current = false;
    writer.flushOnUnload();
    await elapse(0);

    expect(calls.map((call) => [call.target, call.id])).toEqual([[B, undefined]]);
  });

  it("sends positions again once the capability is back", async () => {
    const { send, calls } = controlledSend();
    const { writer, capability } = writerAccepting(send);
    capability.current = false;
    writer.advance(A, at(3));
    await elapse();
    capability.current = true;
    writer.advance(A, at(4));
    await elapse();

    expect(ids(calls)).toEqual(["m4"]);
  });
});

// #1082 seventh review: with the badge following the server's answers, the
// writes must keep pace with a reader who never stops — and never flood.
describe("createReadCursorWriter — a reader scrolling without pause", () => {
  it("writes once per window, whatever the network's speed, and ends on the last position", async () => {
    // A server that answers in 60ms — as fast as the reader reads: every
    // answer finds a newer position waiting.
    const sent: string[] = [];
    const send = vi.fn(
      (_target: ReadTarget, id: string | undefined) =>
        new Promise<ConversationReadState>((resolve) => {
          sent.push(id ?? "all");
          setTimeout(() => resolve(answer(Number(id?.slice(1) ?? 0))), 60);
        }),
    );
    const writer = createReadCursorWriter(send, vi.fn());

    // A new message read every 60ms for three seconds: 50 positions.
    for (let n = 1; n <= 50; n++) {
      writer.advance(A, at(n));
      await elapse(60);
      if (n === 7) expect(sent).toEqual(["m7"]); // the first window was not pushed back
    }
    await elapse();

    expect(sent.length).toBeLessThanOrEqual(Math.ceil(3000 / (READ_CURSOR_DEBOUNCE_MS + 60)) + 1);
    expect(sent.at(-1)).toBe("m50");
    const order = sent.map((id) => Number(id.slice(1)));
    expect([...order].sort((a, b) => a - b)).toEqual(order);
  });
});

// #1082 security review SR-002: a write the server refuses for now — a rate
// limit — is kept, not acknowledged, and not retried in a loop.
describe("createReadCursorWriter — a server that rate-limits", () => {
  const BACKOFF = 60_000;
  const limited = new Error("rate limited");

  function limitedWriter() {
    const { send, calls } = controlledSend();
    const onSettled = vi.fn();
    const writer = createReadCursorWriter(send, onSettled, {
      backoffAfter: (error) => (error === limited ? BACKOFF : undefined),
    });
    return { writer, send, calls, onSettled };
  }

  it("does not acknowledge the refused position, and keeps the greatest pending", async () => {
    const { writer, calls, onSettled } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    writer.advance(A, at(5));
    calls[0].reject(limited);
    await elapse(0);
    expect(onSettled).toHaveBeenLastCalledWith(A, { request: at(3), ok: false });

    // Nothing is sent inside the server's window, however much is read.
    writer.advance(A, at(6));
    await elapse(BACKOFF - 1000);
    expect(ids(calls)).toEqual(["m3"]);
    // Once it passes, the greatest position goes out — once.
    await elapse(1000 + READ_CURSOR_DEBOUNCE_MS);
    expect(ids(calls)).toEqual(["m3", "m6"]);
  });

  it("re-sends the refused position by itself once the window passes, when the reader went idle", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(limited);
    await elapse(BACKOFF - 1000);
    expect(ids(calls)).toEqual(["m3"]);
    // The window passes with nothing read since: the cursor still gets saved.
    await elapse(1000);
    expect(calls.map((call) => [call.id, call.keepalive])).toEqual([
      ["m3", false],
      ["m3", false],
    ]);
  });

  it("retries by itself only once: a refused retry waits for the reader", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(limited);
    await elapse(BACKOFF);
    calls[1].reject(limited);
    await elapse(10 * BACKOFF);
    expect(ids(calls)).toEqual(["m3", "m3"]);
    // A flush (the tab hidden) is the reader's trigger, past the window.
    writer.flush();
    await elapse(0);
    expect(calls.map((call) => [call.id, call.keepalive])).toEqual([
      ["m3", false],
      ["m3", false],
      ["m3", true],
    ]);
  });

  it("earns its one automatic retry again after a write succeeds", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(limited);
    await elapse(BACKOFF);
    calls[1].resolve(answer(3));
    await elapse(0);
    writer.advance(A, at(4));
    await elapse();
    calls[2].reject(limited);
    await elapse(BACKOFF);
    expect(ids(calls)).toEqual(["m3", "m3", "m4", "m4"]);
  });

  it("never retries in a loop while the server keeps refusing", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(limited);
    await elapse(0);
    writer.advance(A, at(4));
    await elapse(BACKOFF + READ_CURSOR_DEBOUNCE_MS);
    calls[1].reject(limited);
    // Ten minutes of nothing: no request storm, no retry at all.
    await elapse(10 * BACKOFF);
    expect(ids(calls)).toEqual(["m3", "m4"]);
  });

  it("does not flush inside the window, and drops everything on dispose", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(limited);
    await elapse(0);
    writer.flush();
    await elapse(0);
    expect(ids(calls)).toEqual(["m3"]);
    writer.advance(A, at(4));
    writer.dispose();
    await elapse(2 * BACKOFF);
    expect(ids(calls)).toEqual(["m3"]);
  });

  it("keeps a refused mark-all, and sends it once the window passes and the reader acts", async () => {
    const { writer, calls } = limitedWriter();
    writer.markAll(A);
    await elapse();
    calls[0].reject(limited);
    await elapse(BACKOFF);
    writer.flush();
    await elapse(0);
    expect(ids(calls)).toEqual([undefined, undefined]);
  });

  it("treats any other failure as before: dropped, not held", async () => {
    const { writer, calls } = limitedWriter();
    writer.advance(A, at(3));
    await elapse();
    calls[0].reject(new Error("offline"));
    await elapse(0);
    writer.advance(A, at(4));
    await elapse();
    expect(ids(calls)).toEqual(["m3", "m4"]);
  });
});

// The cadence the server's read budget is sized from (security review SR-002).
describe("createReadCursorWriter — throughput", () => {
  it("writes about forty times in twenty seconds of continuous reading at a 100ms round trip", async () => {
    const sent: string[] = [];
    const send = vi.fn(
      (_target: ReadTarget, id: string | undefined) =>
        new Promise<ConversationReadState>((resolve) => {
          sent.push(id ?? "all");
          setTimeout(() => resolve(answer(0)), 100);
        }),
    );
    const writer = createReadCursorWriter(send, vi.fn());
    // A new row read every 200ms for twenty seconds.
    for (let n = 0; n < 100; n++) {
      writer.advance(A, {
        id: `r${n}`,
        createdAt: new Date(Date.UTC(2026, 6, 15, 10, 0, n)).toISOString(),
      });
      await elapse(200);
    }
    await elapse(1000);
    // One write per 400ms window plus the round trip: 20s / 0.5s = 40.
    expect(sent.length).toBeGreaterThanOrEqual(38);
    expect(sent.length).toBeLessThanOrEqual(41);
  });
});
