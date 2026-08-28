import { describe, expect, it } from "vitest";

import type { Call } from "./callState";
import {
  convergeResourceCallEvent,
  convergeResourceSyncNull,
  emptyResourceCallDiscovery,
  pruneResourceCallDiscovery,
  resourceKey,
  type ResourceCallDiscoveryState,
} from "./resourceCallDiscovery";

const channelX = { kind: "channel" as const, id: "00000000-0000-4000-8000-0000000000x1" };
const channelY = { kind: "channel" as const, id: "00000000-0000-4000-8000-0000000000y1" };

function call(overrides: Partial<Call> = {}): Call {
  return {
    call_id: "00000000-0000-4000-8000-000000000c01",
    request_id: "00000000-0000-4000-8000-000000000r01",
    caller_id: "00000000-0000-4000-8000-000000000a01",
    callee_id: "",
    target_type: channelX.kind,
    target_id: channelX.id,
    call_type: "audio",
    status: "active",
    version: 1,
    created_at: "2026-08-21T12:00:00Z",
    occurred_at: "2026-08-21T12:00:00Z",
    expires_at: "2026-08-21T12:00:30Z",
    ...overrides,
  };
}

describe("resource call discovery reducer", () => {
  it("1. an active event creates an observation", () => {
    const state = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call(),
    );
    const observed = state.get(resourceKey(channelX.kind, channelX.id));
    expect(observed?.call?.status).toBe("active");
  });

  it("2. same call_id: a higher version wins", () => {
    const first = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ version: 1 }),
    );
    const second = convergeResourceCallEvent(
      first,
      channelX.kind,
      channelX.id,
      call({ version: 2, occurred_at: "2026-08-21T12:00:01Z" }),
    );
    expect(second.get(resourceKey(channelX.kind, channelX.id))?.call?.version).toBe(2);
  });

  it("3. same call_id: a lower version is ignored", () => {
    const first = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ version: 3 }),
    );
    const second = convergeResourceCallEvent(
      first,
      channelX.kind,
      channelX.id,
      call({ version: 2 }),
    );
    expect(second).toBe(first);
  });

  it("4. a higher-version ended hides the active indicator", () => {
    const first = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ version: 1, status: "active" }),
    );
    const ended = convergeResourceCallEvent(
      first,
      channelX.kind,
      channelX.id,
      call({ version: 2, status: "ended", occurred_at: "2026-08-21T12:05:00Z" }),
    );
    expect(ended.get(resourceKey(channelX.kind, channelX.id))?.call?.status).toBe("ended");
  });

  it("5. a stale accepted delivered after ended never resurrects it", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ version: 1, status: "active" }),
    );
    const ended = convergeResourceCallEvent(
      active,
      channelX.kind,
      channelX.id,
      call({ version: 2, status: "ended", occurred_at: "2026-08-21T12:05:00Z" }),
    );
    // A duplicate/delayed "accepted" for the same call_id, same or lower version.
    const staleAccepted = convergeResourceCallEvent(
      ended,
      channelX.kind,
      channelX.id,
      call({ version: 1, status: "active" }),
    );
    expect(staleAccepted).toBe(ended);
    expect(staleAccepted.get(resourceKey(channelX.kind, channelX.id))?.call?.status).toBe("ended");
  });

  it("6. a different call_id with a newer occurred_at wins", () => {
    const first = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-old", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    const second = convergeResourceCallEvent(
      first,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-new", occurred_at: "2026-08-21T12:10:00Z" }),
    );
    expect(second.get(resourceKey(channelX.kind, channelX.id))?.call?.call_id).toBe("call-new");
  });

  it("7. a different (older) call_id never wins", () => {
    const first = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-new", occurred_at: "2026-08-21T12:10:00Z" }),
    );
    const second = convergeResourceCallEvent(
      first,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-old", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    expect(second).toBe(first);
  });

  it("8. a newer null observed_at clears the active call", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ occurred_at: "2026-08-21T12:00:00Z" }),
    );
    const cleared = convergeResourceSyncNull(
      active,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:10:00Z",
    );
    expect(cleared.get(resourceKey(channelX.kind, channelX.id))?.call).toBeNull();
  });

  it("9. an older null observed_at does not clear a newer call", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ occurred_at: "2026-08-21T12:10:00Z" }),
    );
    const notCleared = convergeResourceSyncNull(
      active,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:00:00Z",
    );
    expect(notCleared).toBe(active);
    expect(notCleared.get(resourceKey(channelX.kind, channelX.id))?.call?.status).toBe("active");
  });

  it("10. a call occurring after a null tombstone wins", () => {
    const nulled = convergeResourceSyncNull(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:00:00Z",
    );
    const started = convergeResourceCallEvent(
      nulled,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-after-null", occurred_at: "2026-08-21T12:05:00Z" }),
    );
    expect(started.get(resourceKey(channelX.kind, channelX.id))?.call?.call_id).toBe(
      "call-after-null",
    );
  });

  it("11. a call occurring before a null tombstone does not resurrect", () => {
    const nulled = convergeResourceSyncNull(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:10:00Z",
    );
    const stale = convergeResourceCallEvent(
      nulled,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-before-null", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    expect(stale).toBe(nulled);
    expect(stale.get(resourceKey(channelX.kind, channelX.id))?.call).toBeNull();
  });

  it("12. X and Y coexist independently", () => {
    const withX = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x" }),
    );
    const withXY = convergeResourceCallEvent(
      withX,
      channelY.kind,
      channelY.id,
      call({ call_id: "call-y", target_id: channelY.id }),
    );
    expect(withXY.get(resourceKey(channelX.kind, channelX.id))?.call?.call_id).toBe("call-x");
    expect(withXY.get(resourceKey(channelY.kind, channelY.id))?.call?.call_id).toBe("call-y");
  });

  it("13. X going terminal never removes Y's observation", () => {
    const withX = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 1 }),
    );
    const withXY = convergeResourceCallEvent(
      withX,
      channelY.kind,
      channelY.id,
      call({ call_id: "call-y", target_id: channelY.id }),
    );
    const xEnded = convergeResourceCallEvent(
      withXY,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 2, status: "ended", occurred_at: "2026-08-21T13:00:00Z" }),
    );
    expect(xEnded.get(resourceKey(channelY.kind, channelY.id))?.call?.status).toBe("active");
  });

  it("14. a malformed timestamp never throws and never advances state", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ occurred_at: "2026-08-21T12:00:00Z" }),
    );
    expect(() =>
      convergeResourceSyncNull(active, channelX.kind, channelX.id, "not-a-real-timestamp"),
    ).not.toThrow();
    const result = convergeResourceSyncNull(
      active,
      channelX.kind,
      channelX.id,
      "not-a-real-timestamp",
    );
    expect(result).toBe(active);
  });

  // Issue #622 round 2 adversarial audit (section 4): the malformed-
  // timestamp matrix above only ever exercised "invalid observed_at over an
  // active current" — a malformed candidate occurred_at arriving via
  // convergeResourceCallEvent (a different call_id, not just a null sync)
  // was never tested at all, nor was a malformed timestamp arriving over a
  // TOMBSTONE current (call: null) in either direction.
  it("14b. a malformed occurred_at (different call_id) never advances over an active current", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-active", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    const result = convergeResourceCallEvent(
      active,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-other", occurred_at: "not-a-real-timestamp" }),
    );
    expect(result).toBe(active);
    expect(result.get(resourceKey(channelX.kind, channelX.id))?.call?.call_id).toBe("call-active");
  });

  it("14c. a malformed occurred_at (different call_id) never advances over a tombstone current", () => {
    const tombstone = convergeResourceSyncNull(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:00:00Z",
    );
    const result = convergeResourceCallEvent(
      tombstone,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-new", occurred_at: "not-a-real-timestamp" }),
    );
    expect(result).toBe(tombstone);
    expect(result.get(resourceKey(channelX.kind, channelX.id))?.call).toBeNull();
  });

  it("14d. a malformed observed_at (null sync) never advances over a tombstone current", () => {
    const tombstone = convergeResourceSyncNull(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      "2026-08-21T12:00:00Z",
    );
    const result = convergeResourceSyncNull(
      tombstone,
      channelX.kind,
      channelX.id,
      "not-a-real-timestamp",
    );
    expect(result).toBe(tombstone);
  });

  // Issue #622 round 2 adversarial audit (section 5): version must be the
  // SOLE authority for a same call_id — never a timestamp, in either
  // direction, even an adversarial one that looks "earlier"/"later" than it
  // should. Existing tests 2-5 only ever used timestamps that already agreed
  // with which version was newer; these deliberately disagree.
  it("5b. version wins even when the higher-version candidate's occurred_at looks EARLIER than the current cursor", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 5, occurred_at: "2026-08-21T12:10:00Z" }),
    );
    const ended = convergeResourceCallEvent(
      active,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 6, status: "ended", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    expect(ended.get(resourceKey(channelX.kind, channelX.id))?.call?.status).toBe("ended");
    expect(ended.get(resourceKey(channelX.kind, channelX.id))?.call?.version).toBe(6);
  });

  it("5c. a lower version never resurrects even when its occurred_at looks LATER than the current cursor", () => {
    const ended = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 6, status: "ended", occurred_at: "2026-08-21T12:00:00Z" }),
    );
    const staleActive = convergeResourceCallEvent(
      ended,
      channelX.kind,
      channelX.id,
      call({
        call_id: "call-x",
        version: 5,
        status: "active",
        occurred_at: "2026-08-21T12:10:00Z",
      }),
    );
    expect(staleActive).toBe(ended);
    expect(staleActive.get(resourceKey(channelX.kind, channelX.id))?.call?.status).toBe("ended");
  });

  it("5d. an exact duplicate version resent for the same call_id never changes state", () => {
    const active = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 3 }),
    );
    const resent = convergeResourceCallEvent(
      active,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x", version: 3, occurred_at: "2026-08-21T13:00:00Z" }),
    );
    expect(resent).toBe(active);
  });

  it("15. directory removal purges the target's observation", () => {
    const withX = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call({ call_id: "call-x" }),
    );
    const withXY = convergeResourceCallEvent(
      withX,
      channelY.kind,
      channelY.id,
      call({ call_id: "call-y", target_id: channelY.id }),
    );
    const onlyYKnown = new Set([resourceKey(channelY.kind, channelY.id)]);
    const pruned = pruneResourceCallDiscovery(withXY, onlyYKnown);
    expect(pruned.has(resourceKey(channelX.kind, channelX.id))).toBe(false);
    expect(pruned.get(resourceKey(channelY.kind, channelY.id))?.call?.call_id).toBe("call-y");
  });

  it("prune is a no-op (same reference) when nothing needs removing", () => {
    const withX = convergeResourceCallEvent(
      emptyResourceCallDiscovery,
      channelX.kind,
      channelX.id,
      call(),
    );
    const stillEverything = pruneResourceCallDiscovery(
      withX,
      new Set([resourceKey(channelX.kind, channelX.id)]),
    );
    expect(stillEverything).toBe(withX);
  });

  it("does not mutate the input state (structural sharing, never in-place)", () => {
    const state: ResourceCallDiscoveryState = emptyResourceCallDiscovery;
    const next = convergeResourceCallEvent(state, channelX.kind, channelX.id, call());
    expect(state.size).toBe(0);
    expect(next.size).toBe(1);
  });
});
