/**
 * presence store tests (RF-58).
 *
 * Two layers, deliberately separated:
 *  - the reducer, exercised as a pure function, where every ordering,
 *    duplication and malformed-payload case is stated directly;
 *  - the store and hook, exercised through a fake socket, where what is asserted
 *    is what a component would actually render.
 *
 * Nothing here reaches into store internals: the questions are "what does the
 * avatar say" and "does the same event reaching two consumers move both".
 */

import { render, screen, act } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import {
  _resetChatSocket,
  getChatSocketStatus,
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_DELAY_MS,
} from "./chatSocket";
import {
  _resetPresenceStore,
  applyPresenceUpdate,
  comparePresenceInstant,
  emptyPresenceState,
  parsePresenceInstant,
  presenceLabel,
  reducePresence,
  presenceTargetKey,
  selectPresence,
  usePresence,
  type PresenceSnapshotState,
} from "./presence";
import PresenceDot from "./PresenceDot";

// ── fake socket ──────────────────────────────────────────────────────────────

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close(code = 1006) {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code }));
  }

  open() {
    this.onopen?.();
  }

  emit(data: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
}

const OriginalWebSocket = global.WebSocket;

function socket(): FakeWebSocket {
  const instance = FakeWebSocket.instances.at(-1);
  if (!instance) throw new Error("no socket was created");
  return instance;
}

function openSocket(): void {
  act(() => {
    socket().open();
  });
}

function deliver(frame: unknown): void {
  act(() => {
    socket().emit(frame);
  });
}

/** A complete snapshot for one conversation, which is what the server sends. */
function snapshotFrame(users: unknown[], targetId = "chan-1") {
  return {
    type: "presence.snapshot",
    target_type: "channel",
    target_id: targetId,
    users,
    complete: true,
    taken_at: SNAPSHOT_TAKEN_AT,
  };
}

/** The conversation snapshotFrame describes, in the form callers ask about. */
function target(targetId = "chan-1") {
  return presenceTargetKey("channel", targetId);
}

/** A presence event with no instant at all, which the server may legitimately send. */
function untimedUpdateFrame(userId: string, state: string, targetId = "chan-1") {
  return {
    type: "presence.updated",
    target_type: "channel",
    target_id: targetId,
    presence: { user_id: userId, state },
  };
}

function updateFrame(userId: string, state: string, updatedAt: string, targetId = "chan-1") {
  return {
    type: "presence.updated",
    target_type: "channel",
    target_id: targetId,
    presence: { user_id: userId, state, updated_at: updatedAt },
  };
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
  _resetChatSocket(() => 0);
  _resetPresenceStore();
  vi.useFakeTimers();
});

afterEach(() => {
  _resetPresenceStore();
  _resetChatSocket();
  vi.useRealTimers();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.restoreAllMocks();
});

// ── reducer ──────────────────────────────────────────────────────────────────

const T1 = "2026-08-11T10:00:00.000Z";
/** Later than every fixture instant, so a snapshot is normally the newest word. */
const SNAPSHOT_TAKEN_AT = "2026-08-11T23:00:00.000Z";
const T2 = "2026-08-11T10:00:05.000Z";
const T3 = "2026-08-11T10:00:10.000Z";

function state(...frames: Record<string, unknown>[]): PresenceSnapshotState {
  return frames.reduce(reducePresence, emptyPresenceState);
}

describe("reducePresence", () => {
  it("applies a snapshot and records that conversation as covered", () => {
    const next = state(
      snapshotFrame([
        { user_id: "u-1", state: "online", updated_at: T1 },
        { user_id: "u-2", state: "away", updated_at: T1 },
      ]),
    );

    expect(selectPresence(next, "u-1", target())).toBe("online");
    expect(selectPresence(next, "u-2", target())).toBe("away");
    expect(next.covered.has(target())).toBe(true);
  });

  it("reads a user missing from a covered conversation as offline", () => {
    expect(selectPresence(emptyPresenceState, "u-absent", target())).toBe("unknown");
    expect(selectPresence(state(snapshotFrame([])), "u-absent", target())).toBe("offline");
  });

  // CQ-1. The snapshot for one conversation is not an answer about another, and
  // it is not an answer about a person who was never eligible to appear in it.
  it("does not let one conversation's snapshot resolve a user in another", () => {
    const next = state(snapshotFrame([], "chan-a"));

    expect(selectPresence(next, "u-elsewhere", target("chan-b"))).toBe("unknown");
    expect(next.covered.has(target("chan-b"))).toBe(false);
    // And with no conversation named at all, nothing may be concluded either.
    expect(selectPresence(next, "u-elsewhere")).toBe("unknown");
    // The conversation it *did* describe is resolved.
    expect(selectPresence(next, "u-elsewhere", target("chan-a"))).toBe("offline");
  });

  it("never infers offline from a snapshot the server marked incomplete", () => {
    const next = state({
      type: "presence.snapshot",
      target_type: "channel",
      target_id: "chan-1",
      users: [{ user_id: "u-1", state: "online", updated_at: T1 }],
      complete: false,
    });

    // The entries it carried are still true: each names one person.
    expect(selectPresence(next, "u-1", target())).toBe("online");
    // But absence from a list that was cut short means nothing.
    expect(selectPresence(next, "u-2", target())).toBe("unknown");
    expect(next.covered.has(target())).toBe(false);
  });

  it("treats a snapshot with no completeness flag as incomplete", () => {
    const next = state({
      type: "presence.snapshot",
      target_type: "channel",
      target_id: "chan-1",
      users: [],
    });

    expect(selectPresence(next, "u-1", target())).toBe("unknown");
  });

  // An explicit statement resolves a user in the conversation it was made about,
  // with no coverage needed — the server named them and named their state — and
  // says nothing about a conversation it was not about.
  it("applies an explicit offline event within its own conversation", () => {
    const next = state(updateFrame("u-1", "offline", T1));

    expect(selectPresence(next, "u-1", target())).toBe("offline");
    // No conversation named: answered from whatever evidence exists.
    expect(selectPresence(next, "u-1")).toBe("offline");
    // A conversation nobody has said anything about stays unknown.
    expect(selectPresence(next, "u-1", target("chan-anything"))).toBe("unknown");
  });

  it("applies a newer update over an older one", () => {
    const next = state(
      snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]),
      updateFrame("u-1", "away", T2),
    );

    expect(selectPresence(next, "u-1", target())).toBe("away");
  });

  it("discards an update older than what is already applied", () => {
    const current = state(updateFrame("u-1", "away", T2));
    const next = reducePresence(current, updateFrame("u-1", "online", T1));

    expect(selectPresence(next, "u-1", target())).toBe("away");
    // Unchanged state is returned identically so React can skip the render.
    expect(next).toBe(current);
  });

  it("treats a duplicate update as a no-op", () => {
    const current = state(updateFrame("u-1", "online", T1));
    const next = reducePresence(current, updateFrame("u-1", "online", T1));

    expect(next).toBe(current);
    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  it("does not let a stale snapshot overwrite a newer event", () => {
    // The race the reconnect sequence can produce: an event for the new state
    // arrives while a snapshot computed before it is still in flight.
    const current = state(updateFrame("u-1", "offline", T3));
    const next = reducePresence(
      current,
      snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]),
    );

    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  it("ignores frames that are not presence", () => {
    const current = state(updateFrame("u-1", "online", T1));
    for (const frame of [
      { type: "message.created", target_id: "chan-1" },
      { type: "subscribed", operation: "subscribe" },
    ]) {
      expect(reducePresence(current, frame)).toBe(current);
    }
  });

  it("ignores a payload with a state outside the contract", () => {
    const current = state(snapshotFrame([]));
    for (const frame of [
      updateFrame("u-1", "busy", T1),
      updateFrame("", "online", T1),
      { type: "presence.updated" },
      { type: "presence.updated", presence: "online" },
    ]) {
      expect(reducePresence(current, frame)).toBe(current);
      expect(selectPresence(reducePresence(current, frame), "u-1", target())).toBe("offline");
    }
  });

  it("skips malformed entries inside a snapshot without losing the good ones", () => {
    const next = state(
      snapshotFrame([
        { user_id: "u-1", state: "online", updated_at: T1 },
        { user_id: "u-2", state: "elsewhere", updated_at: T1 },
        null,
        "nonsense",
      ]),
    );

    expect(selectPresence(next, "u-1", target())).toBe("online");
    // Present in the payload but unusable: read as offline like anyone absent,
    // never as the invented state.
    expect(selectPresence(next, "u-2", target())).toBe("offline");
  });

  it("takes the latest arrival when the server stated no instant", () => {
    // No ordering information at all: first-wins would freeze the value forever.
    const next = state(untimedUpdateFrame("u-1", "online"), untimedUpdateFrame("u-1", "away"));

    expect(selectPresence(next, "u-1", target())).toBe("away");
  });

  it("never lets an unparseable instant displace a real one", () => {
    const current = state(updateFrame("u-1", "online", T2));
    const next = reducePresence(current, {
      type: "presence.updated",
      target_type: "channel",
      target_id: "chan-1",
      presence: { user_id: "u-1", state: "away", updated_at: "not a date" },
    });

    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  it("returns the same map when an update changes nothing", () => {
    const entries = state(updateFrame("u-1", "online", T1)).entries.get(target())!;
    expect(
      applyPresenceUpdate(entries, "u-1", {
        state: "online",
        updatedAt: parsePresenceInstant(T1)!,
      }),
    ).toBe(entries);
  });
});

// ── nanosecond ordering (RFC 3339 Nano) ──────────────────────────────────────

// The server stamps presence to the nanosecond, and two transitions of the same
// user can share a millisecond. Read at millisecond precision they tie, and a
// tie is read as a duplicate — which silently discarded the newer state.

/** Same second, same millisecond, different nanosecond. */
const NANO_A = "2026-08-11T10:00:00.123000001Z";
const NANO_B = "2026-08-11T10:00:00.123000002Z";
const NANO_LAST = "2026-08-11T10:00:00.123999999Z";

describe("RFC 3339 Nano instants", () => {
  it("keeps the fraction whole, whatever its length", () => {
    // .1 and .10 and .100000000 are one instant; the padding is what makes the
    // comparison agree with that.
    const tenth = parsePresenceInstant("2026-08-11T10:00:00.1Z");
    expect(tenth).toEqual({
      secondMs: Date.parse("2026-08-11T10:00:00Z"),
      nanosecond: 100_000_000,
    });
    expect(parsePresenceInstant("2026-08-11T10:00:00.10Z")).toEqual(tenth);
    expect(parsePresenceInstant("2026-08-11T10:00:00.100000000Z")).toEqual(tenth);

    expect(parsePresenceInstant("2026-08-11T10:00:00.100000001Z")!.nanosecond).toBe(100_000_001);
    expect(parsePresenceInstant("2026-08-11T10:00:00.999999999Z")!.nanosecond).toBe(999_999_999);
    // No fraction at all is the start of the second, not an unknown instant.
    expect(parsePresenceInstant("2026-08-11T10:00:00Z")!.nanosecond).toBe(0);
  });

  it("applies the zone before comparing, not after", () => {
    expect(parsePresenceInstant("2026-08-11T13:00:00+03:00")).toEqual(
      parsePresenceInstant("2026-08-11T10:00:00Z"),
    );
    expect(parsePresenceInstant("2026-08-11T07:00:00-03:00")).toEqual(
      parsePresenceInstant("2026-08-11T10:00:00Z"),
    );
  });

  it("refuses anything that is not a timestamp", () => {
    for (const bad of [
      "",
      "not a date",
      "2026-08-11T10:00:00", // no zone: RFC 3339 requires one
      "2026-08-11T10:00:00.1234567890Z", // ten fractional digits
      "2026-13-11T10:00:00Z", // month 13
      "2026-08-11T10:00:00Z ",
      42,
      null,
      undefined,
    ]) {
      expect(parsePresenceInstant(bad)).toBeNull();
    }
  });

  it("orders inside one millisecond", () => {
    const a = parsePresenceInstant(NANO_A)!;
    const b = parsePresenceInstant(NANO_B)!;
    expect(comparePresenceInstant(a, b)).toBe(-1);
    expect(comparePresenceInstant(b, a)).toBe(1);
    expect(comparePresenceInstant(a, parsePresenceInstant(NANO_A)!)).toBe(0);
  });

  it("takes the newer of two transitions in the same millisecond", () => {
    const next = state(updateFrame("u-1", "online", NANO_A), updateFrame("u-1", "away", NANO_B));

    expect(selectPresence(next, "u-1", target())).toBe("away");
  });

  it("takes the newer one across the whole millisecond", () => {
    const next = state(
      updateFrame("u-1", "online", NANO_A),
      updateFrame("u-1", "offline", NANO_LAST),
    );

    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  it("discards an event that is stale by nanoseconds", () => {
    const next = state(updateFrame("u-1", "online", NANO_LAST), updateFrame("u-1", "away", NANO_A));

    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  it("does not let a snapshot read earlier in the same millisecond undo an event", () => {
    const next = state(updateFrame("u-1", "online", NANO_LAST), snapshotAt([], NANO_A));

    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  it("lets a snapshot read later in the same millisecond correct the state", () => {
    const next = state(updateFrame("u-1", "online", NANO_A), snapshotAt([], NANO_LAST));

    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });
});

// ── complete snapshots replace a conversation's view (CQ-3) ─────────────────

/** A snapshot with an explicit read instant, for the ordering cases. */
function snapshotAt(users: unknown[], takenAt: string, targetId = "chan-1", complete = true) {
  return {
    type: "presence.snapshot",
    target_type: "channel",
    target_id: targetId,
    users,
    complete,
    taken_at: takenAt,
  };
}

describe("complete snapshot replacement", () => {
  it("removes a user the new snapshot no longer names", () => {
    const next = state(
      snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]),
      snapshotAt([], "2026-08-12T00:00:00.000Z"),
    );

    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  it("keeps the people it still names while removing the rest", () => {
    const next = state(
      snapshotFrame([
        { user_id: "u-1", state: "online", updated_at: T1 },
        { user_id: "u-2", state: "away", updated_at: T1 },
      ]),
      snapshotAt([{ user_id: "u-2", state: "away", updated_at: T1 }], "2026-08-12T00:00:00.000Z"),
    );

    expect(selectPresence(next, "u-2", target())).toBe("away");
    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  it("leaves other conversations untouched", () => {
    const next = state(
      snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }], "chan-a"),
      snapshotAt([], "2026-08-12T00:00:00.000Z", "chan-b"),
    );

    // The empty answer about B says nothing about A.
    expect(selectPresence(next, "u-1", target("chan-a"))).toBe("online");
    expect(selectPresence(next, "u-1", target("chan-b"))).toBe("offline");
  });

  it("does not let a snapshot read before an event undo it", () => {
    const next = state(
      updateFrame("u-1", "online", "2026-08-11T10:00:20.000Z"),
      // Read ten seconds *before* that transition happened.
      snapshotAt([], "2026-08-11T10:00:10.000Z"),
    );

    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  it("applies a snapshot read after the event", () => {
    const next = state(
      updateFrame("u-1", "online", "2026-08-11T10:00:10.000Z"),
      snapshotAt([], "2026-08-11T10:00:20.000Z"),
    );

    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  it("never removes anybody on an incomplete snapshot", () => {
    const next = state(
      snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]),
      snapshotAt([], "2026-08-12T00:00:00.000Z", "chan-1", false),
    );

    expect(selectPresence(next, "u-1", target())).toBe("online");
  });

  // Coverage is a claim about what the server can see *now*. The directory
  // degrades, the same conversation starts answering `complete: false`, and a
  // coverage granted a minute ago would go on turning every omission into a
  // confident offline — with nothing that could ever contradict it, because an
  // omission produces no later frame.
  it("gives up coverage when the server stops answering completely", () => {
    const covered = state(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(covered.covered.has(target())).toBe(true);
    expect(selectPresence(covered, "u-2", target())).toBe("offline");

    const degraded = reducePresence(
      covered,
      snapshotAt(
        [{ user_id: "u-1", state: "online", updated_at: T1 }],
        SNAPSHOT_TAKEN_AT,
        "chan-1",
        false,
      ),
    );

    expect(degraded.covered.has(target())).toBe(false);
    // Nothing may be concluded about who the shortened list left out.
    expect(selectPresence(degraded, "u-2", target())).toBe("unknown");
    // What it did name is still true, and nobody was removed.
    expect(selectPresence(degraded, "u-1", target())).toBe("online");
  });

  // The degraded answer carrying nothing new is exactly the case the old
  // short-circuit swallowed: no entry moved, so the state was returned
  // untouched — coverage included.
  it("gives up coverage even when the incomplete snapshot changes no entry", () => {
    const degraded = state(snapshotFrame([]), snapshotAt([], SNAPSHOT_TAKEN_AT, "chan-1", false));

    expect(degraded.covered.has(target())).toBe(false);
    expect(selectPresence(degraded, "u-1", target())).toBe("unknown");
  });

  it("only withdraws coverage from the conversation that degraded", () => {
    const next = state(
      snapshotFrame([], "chan-a"),
      snapshotFrame([], "chan-b"),
      snapshotAt([], SNAPSHOT_TAKEN_AT, "chan-b", false),
    );

    expect(selectPresence(next, "u-1", target("chan-a"))).toBe("offline");
    expect(selectPresence(next, "u-1", target("chan-b"))).toBe("unknown");
  });

  it("takes coverage back when the server can answer completely again", () => {
    const next = state(
      snapshotFrame([]),
      snapshotAt([], SNAPSHOT_TAKEN_AT, "chan-1", false),
      snapshotAt([], "2026-08-12T00:00:00.000Z"),
    );

    expect(next.covered.has(target())).toBe(true);
    expect(selectPresence(next, "u-1", target())).toBe("offline");
  });

  // The whole point of the server's reconciliation: a node dies, its users stop
  // appearing in the roster, and an observer who never reconnects converges.
  it("converges a stale online through reconciliation", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    // No reconnect, no reload, no new transition from u-1 — only the server's
    // corrected roster for that conversation.
    deliver(snapshotAt([], "2026-08-12T00:00:00.000Z"));

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "offline");
  });
});

// ── hook and store ───────────────────────────────────────────────────────────

function Indicator({
  userId,
  label,
  within = target(),
}: {
  userId: string;
  label: string;
  /** The conversation this avatar is rendered in; null renders one outside any. */
  within?: string | null;
}) {
  const presence = usePresence(userId, within ?? undefined);
  return (
    <span data-testid={label} data-state={presence}>
      {presenceLabel(presence)}
      <PresenceDot state={presence} />
    </span>
  );
}

describe("usePresence", () => {
  it("starts unknown and never claims offline before the server has answered", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
    expect(screen.getByTestId("a")).toHaveTextContent("Status indisponível");
  });

  it("applies the snapshot and then incremental updates without a reload", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();

    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    deliver(updateFrame("u-1", "away", T2));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "away");
    expect(screen.getByTestId("a")).toHaveTextContent("Ausente");

    deliver(updateFrame("u-1", "offline", T3));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "offline");
  });

  it("moves every consumer of the same user at once", () => {
    render(
      <>
        <Indicator userId="u-1" label="sidebar" />
        <Indicator userId="u-1" label="panel" />
        <Indicator userId="u-2" label="other" />
      </>,
    );
    openSocket();
    deliver(
      snapshotFrame([
        { user_id: "u-1", state: "online", updated_at: T1 },
        { user_id: "u-2", state: "online", updated_at: T1 },
      ]),
    );

    deliver(updateFrame("u-1", "away", T2));

    expect(screen.getByTestId("sidebar")).toHaveAttribute("data-state", "away");
    expect(screen.getByTestId("panel")).toHaveAttribute("data-state", "away");
    // One person changing does not disturb anybody else's row.
    expect(screen.getByTestId("other")).toHaveAttribute("data-state", "online");
  });

  it("discards a stale update arriving after a newer one", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();

    deliver(updateFrame("u-1", "offline", T3));
    deliver(updateFrame("u-1", "online", T1));

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "offline");
  });

  it("rebuilds from the new snapshot after a reconnect instead of trusting old state", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    // The connection drops. Nothing new may be inferred from absence, but the
    // last known state is not thrown away for a blink either.
    act(() => {
      socket().close();
    });
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    // The shared connection reconnects on its own schedule. What was learned
    // over the dead socket is dropped when the new one opens, because the
    // server does not replay what happened while this tab was away.
    act(() => {
      vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS);
    });
    expect(FakeWebSocket.instances).toHaveLength(2);
    openSocket();
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");

    deliver(snapshotFrame([]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "offline");
  });

  // Through the hook: a conversation that degrades stops drawing grey dots for
  // people it can no longer account for, instead of asserting they left.
  it("stops reading absence as offline once a conversation degrades", () => {
    render(<Indicator userId="u-gone" label="a" />);
    openSocket();

    deliver(snapshotFrame([{ user_id: "u-here", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "offline");

    deliver(
      snapshotAt(
        [{ user_id: "u-here", state: "online", updated_at: T1 }],
        SNAPSHOT_TAKEN_AT,
        "chan-1",
        false,
      ),
    );

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  // A connection that has stopped trying is the end of the line: no reconnect,
  // no snapshot, no event. Anything still on screen would be a claim about a
  // person that nothing could ever correct, for as long as the tab is open.
  it("forgets everything when the connection gives up for good", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    // The server rejects the connection permanently, which stops the reconnect
    // loop rather than backing off.
    act(() => {
      socket().close(1008);
    });

    expect(getChatSocketStatus()).toBe("failed");
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
    // And no reconnect is coming to refill it.
    act(() => {
      vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS);
    });
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
  });

  // A transient drop is not that: a reconnect is still coming, so the last known
  // state is worth holding on to for the blink it takes.
  it("keeps what it knows across a drop that will be retried", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));

    act(() => {
      socket().close();
    });

    expect(getChatSocketStatus()).not.toBe("failed");
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");
  });

  it("keeps what it knows when consumers remount on a connection that never dropped", () => {
    // A route change unmounts every avatar and mounts a new set. The shared
    // socket is refcounted and outlives that, so nothing was missed — and
    // nothing may be forgotten either, because a snapshot only arrives with a
    // subscribe and no resubscribe happens for a socket that stayed open.
    const first = render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "away", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "away");

    // A second consumer holds the connection open across the swap, exactly as
    // the sidebar does while the message list is replaced.
    render(<Indicator userId="u-1" label="b" />);
    first.unmount();
    render(<Indicator userId="u-1" label="c" />);

    expect(screen.getByTestId("c")).toHaveAttribute("data-state", "away");
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("stops listening when the last consumer unmounts", () => {
    const view = render(<Indicator userId="u-1" label="a" />);
    openSocket();
    expect(FakeWebSocket.instances).toHaveLength(1);

    view.unmount();

    // The shared connection is released, so nothing is left subscribed and a
    // later frame cannot reach a store nobody is reading.
    expect(socket().readyState).toBe(FakeWebSocket.CLOSED);
  });

  // CQ-1, through the hook: two conversations, and an empty answer about one of
  // them must not decide anything about a person in the other.
  it("keeps a user in another conversation unknown when this one comes back empty", () => {
    render(
      <>
        <Indicator userId="u-here" label="here" within={target("chan-a")} />
        <Indicator userId="u-there" label="there" within={target("chan-b")} />
      </>,
    );
    openSocket();

    deliver(snapshotFrame([], "chan-a"));

    expect(screen.getByTestId("here")).toHaveAttribute("data-state", "offline");
    // Nothing has been said about chan-b, so nothing is claimed about its person.
    expect(screen.getByTestId("there")).toHaveAttribute("data-state", "unknown");
    expect(screen.getAllByTestId("presence-dot")).toHaveLength(1);

    // The answer for chan-b arrives and only then does its row resolve.
    deliver(snapshotFrame([], "chan-b"));
    expect(screen.getByTestId("there")).toHaveAttribute("data-state", "offline");
  });

  // An avatar rendered outside any conversation — the viewer's own row in the
  // sidebar footer — never turns grey on someone else's coverage.
  it("never claims offline for an avatar with no conversation", () => {
    render(<Indicator userId="u-1" label="a" within={null} />);
    openSocket();

    deliver(snapshotFrame([]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");

    // It still follows an explicit statement about that person.
    deliver(updateFrame("u-1", "online", T1));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");
  });

  it("reports unknown for a user with no id", () => {
    render(<Indicator userId="" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
  });
});

// ── session scope (CQ-4) ─────────────────────────────────────────────────────

describe("presence scope", () => {
  it("clears on logout, without waiting for a socket to open", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");

    // Logging out. No socket opens after this — that is the whole point.
    act(() => {
      clearTokens();
    });

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  it("does not show the previous session's presence when the next one fails to connect", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));

    // A different account signs in and its connection never opens.
    act(() => {
      setTokens("another-session-token");
    });
    act(() => {
      socket().close();
    });

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
  });

  it("ignores a frame that arrives from the previous session's socket", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    const previous = socket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T1 }]));

    act(() => {
      setTokens("another-session-token");
    });
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");

    // The old socket answers late. It belongs to a scope that no longer exists.
    act(() => {
      previous.emit(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T2 }]));
    });

    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");
  });

  it("keeps its reconnect behaviour for the same session", () => {
    render(<Indicator userId="u-1" label="a" />);
    openSocket();
    deliver(snapshotFrame([{ user_id: "u-1", state: "away", updated_at: T1 }]));

    // A transient network drop, same session throughout.
    act(() => {
      socket().close();
    });
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "away");

    act(() => {
      vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS);
    });
    openSocket();
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "unknown");

    deliver(snapshotFrame([{ user_id: "u-1", state: "online", updated_at: T2 }]));
    expect(screen.getByTestId("a")).toHaveAttribute("data-state", "online");
  });
});

// ── the indicator ────────────────────────────────────────────────────────────

describe("PresenceDot", () => {
  it("carries the state as data, a class and a tooltip", () => {
    for (const [presence, label] of [
      ["online", "Online"],
      ["away", "Ausente"],
      ["offline", "Offline"],
    ] as const) {
      const view = render(<PresenceDot state={presence} />);
      const dot = screen.getByTestId("presence-dot");

      expect(dot).toHaveAttribute("data-presence", presence);
      expect(dot).toHaveAttribute("title", label);
      expect(dot.className).toContain(`presence-dot--${presence}`);
      // Decoration: the word is supplied by whoever places the dot, so it is
      // never announced twice for one person.
      expect(dot).toHaveAttribute("aria-hidden", "true");

      view.unmount();
    }
  });

  it("renders nothing at all while the state is unknown", () => {
    render(<PresenceDot state="unknown" />);
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
  });

  it("keeps its size classes independent of the state", () => {
    const view = render(<PresenceDot state="online" size="lg" />);
    expect(screen.getByTestId("presence-dot").className).toContain("presence-dot--lg");
    view.unmount();

    render(<PresenceDot state="offline" size="lg" />);
    expect(screen.getByTestId("presence-dot").className).toContain("presence-dot--lg");
  });
});
