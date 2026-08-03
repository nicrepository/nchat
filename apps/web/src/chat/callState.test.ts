import { describe, expect, it } from "vitest";

import { applyCallEvent, initialCallState, type CallEvent } from "./callState";

const call = {
  call_id: "00000000-0000-4000-8000-000000000201",
  request_id: "00000000-0000-4000-8000-000000000202",
  caller_id: "00000000-0000-4000-8000-000000000203",
  callee_id: "00000000-0000-4000-8000-000000000204",
  call_type: "video" as const,
  status: "ringing" as const,
  version: 1,
  created_at: "2026-07-30T12:00:00Z",
  occurred_at: "2026-07-30T12:00:00Z",
  expires_at: "2026-07-30T12:00:30Z",
};

function event(status: CallEvent["call"]["status"], version: number): CallEvent {
  const typeByStatus = {
    ringing: "call.ringing",
    active: "call.accepted",
    declined: "call.declined",
    cancelled: "call.cancelled",
    timed_out: "call.timed_out",
    ended: "call.ended",
  } as const;
  return {
    type: typeByStatus[status],
    event_id: `00000000-0000-4000-8000-00000000020${version}`,
    target_type: "user",
    target_id: call.callee_id,
    call: { ...call, status, version },
  };
}

describe("call state", () => {
  it("applies authoritative versions and ignores duplicates or out-of-order events", () => {
    const ringing = applyCallEvent(initialCallState, event("ringing", 1));
    const active = applyCallEvent(ringing, event("active", 2));
    expect(active.call?.status).toBe("active");
    expect(applyCallEvent(active, event("ringing", 1))).toBe(active);
    expect(applyCallEvent(active, event("active", 2))).toBe(active);
  });

  it("keeps terminal calls immutable against delayed events", () => {
    const timedOut = applyCallEvent(
      applyCallEvent(initialCallState, event("ringing", 1)),
      event("timed_out", 2),
    );
    expect(applyCallEvent(timedOut, event("active", 3))).toBe(timedOut);
  });
});
