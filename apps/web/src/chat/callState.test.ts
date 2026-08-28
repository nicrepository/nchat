import { describe, expect, it } from "vitest";

import {
  applyCallEvent,
  initialCallState,
  parseCall,
  parseCallEvent,
  type CallEvent,
} from "./callState";

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

// Issue #622 round 2 adversarial audit (section 3): parseCall/parseCallEvent
// had zero direct unit test coverage — every existing test exercised them
// only indirectly through higher-level signaling/provider tests. These
// cover the structural validation boundary itself.
const resourceCall = {
  call_id: "00000000-0000-4000-8000-000000000301",
  request_id: "00000000-0000-4000-8000-000000000302",
  caller_id: "00000000-0000-4000-8000-000000000303",
  callee_id: "",
  target_type: "channel" as const,
  target_id: "00000000-0000-4000-8000-000000000304",
  call_type: "audio" as const,
  status: "active" as const,
  version: 1,
  created_at: "2026-08-21T12:00:00Z",
  occurred_at: "2026-08-21T12:00:00Z",
  expires_at: "2026-08-21T12:00:30Z",
};

const directCall = {
  call_id: "00000000-0000-4000-8000-000000000305",
  request_id: "00000000-0000-4000-8000-000000000306",
  caller_id: "00000000-0000-4000-8000-000000000307",
  callee_id: "00000000-0000-4000-8000-000000000308",
  call_type: "video" as const,
  status: "ringing" as const,
  version: 1,
  created_at: "2026-08-21T12:00:00Z",
  occurred_at: "2026-08-21T12:00:00Z",
  expires_at: "2026-08-21T12:00:30Z",
};

describe("parseCall", () => {
  it("accepts a well-formed resource (channel/dm) call", () => {
    expect(parseCall(resourceCall)).toEqual(resourceCall);
  });

  it("accepts a well-formed direct call (no target_type) and normalizes callee_id", () => {
    expect(parseCall(directCall)).toEqual(directCall);
  });

  it("rejects a non-object / null value", () => {
    expect(parseCall(null)).toBeNull();
    expect(parseCall(undefined)).toBeNull();
    expect(parseCall("call")).toBeNull();
    expect(parseCall(42)).toBeNull();
  });

  it("rejects a malformed call_id (missing or wrong type)", () => {
    expect(parseCall({ ...resourceCall, call_id: undefined })).toBeNull();
    expect(parseCall({ ...resourceCall, call_id: 123 })).toBeNull();
  });

  it("rejects a malformed status (unknown value)", () => {
    expect(parseCall({ ...resourceCall, status: "waiting" })).toBeNull();
    expect(parseCall({ ...resourceCall, status: undefined })).toBeNull();
  });

  it("rejects a NaN, negative, or non-integer version", () => {
    expect(parseCall({ ...resourceCall, version: Number.NaN })).toBeNull();
    expect(parseCall({ ...resourceCall, version: -1 })).toBeNull();
    expect(parseCall({ ...resourceCall, version: 1.5 })).toBeNull();
    expect(parseCall({ ...resourceCall, version: "1" })).toBeNull();
  });

  it("accepts version 0 — the first ringing event of a call is legitimately version 0", () => {
    expect(parseCall({ ...resourceCall, version: 0 })).not.toBeNull();
  });

  it("rejects a missing occurred_at (or any other required timestamp)", () => {
    expect(parseCall({ ...resourceCall, occurred_at: undefined })).toBeNull();
    expect(parseCall({ ...resourceCall, created_at: undefined })).toBeNull();
    expect(parseCall({ ...resourceCall, expires_at: undefined })).toBeNull();
  });

  it("still accepts a structurally-unparseable-as-a-date occurred_at string — that is resourceCallDiscovery's job to fail closed on, not parseCall's", () => {
    // parseCall only checks typeof === "string"; a string that Date.parse
    // cannot read is a discovery-reducer concern (see
    // resourceCallDiscovery.test.ts's malformed-timestamp matrix), not a
    // structural validity concern here.
    expect(parseCall({ ...resourceCall, occurred_at: "not-a-real-timestamp" })).not.toBeNull();
  });

  it("rejects a resource call (target_type channel/dm) missing target_id", () => {
    expect(parseCall({ ...resourceCall, target_id: undefined })).toBeNull();
  });

  it("rejects a direct call (target_type user/undefined) missing callee_id", () => {
    expect(parseCall({ ...directCall, callee_id: undefined })).toBeNull();
    expect(parseCall({ ...directCall, target_type: "user", callee_id: undefined })).toBeNull();
  });
});

describe("parseCallEvent", () => {
  function resourceEvent(overrides: Record<string, unknown> = {}) {
    return {
      type: "call.accepted",
      event_id: "00000000-0000-4000-8000-000000000309",
      target_type: "channel",
      target_id: resourceCall.target_id,
      call: resourceCall,
      ...overrides,
    };
  }

  function directEvent(overrides: Record<string, unknown> = {}) {
    return {
      type: "call.ringing",
      event_id: "00000000-0000-4000-8000-00000000030a",
      target_type: "user",
      target_id: directCall.callee_id,
      call: directCall,
      ...overrides,
    };
  }

  it("accepts a well-formed resource event", () => {
    const event = parseCallEvent(resourceEvent());
    expect(event?.call).toEqual(resourceCall);
  });

  it("accepts a well-formed direct event", () => {
    const event = parseCallEvent(directEvent());
    expect(event?.call).toEqual(directCall);
  });

  it("rejects an invalid event type", () => {
    expect(parseCallEvent(resourceEvent({ type: "call.admitted" }))).toBeNull();
  });

  it("rejects an invalid envelope target_type", () => {
    expect(parseCallEvent(resourceEvent({ target_type: "workspace" }))).toBeNull();
  });

  it("rejects a missing event_id", () => {
    expect(parseCallEvent(resourceEvent({ event_id: undefined }))).toBeNull();
  });

  it("rejects a structurally invalid call payload (delegated to parseCall)", () => {
    expect(parseCallEvent(resourceEvent({ call: { ...resourceCall, version: -1 } }))).toBeNull();
  });

  it("rejects a resource envelope/payload target_id mismatch", () => {
    expect(
      parseCallEvent(resourceEvent({ target_id: "00000000-0000-4000-8000-0000000009ff" })),
    ).toBeNull();
  });

  it("rejects a resource envelope whose payload target_type disagrees (dm envelope, channel payload)", () => {
    expect(parseCallEvent(resourceEvent({ target_type: "dm" }))).toBeNull();
  });

  it("rejects a user-targeted envelope whose payload lacks callee_id, even if the payload's own target_type would otherwise pass", () => {
    // The envelope says target_type: "user" (direct), so a real callee_id is
    // still required independently of whatever the payload's own target_type
    // says — this is the exact envelope-vs-payload coherence check the
    // parseCall extraction had to preserve.
    expect(
      parseCallEvent(
        directEvent({ call: { ...resourceCall, target_type: "channel", callee_id: undefined } }),
      ),
    ).toBeNull();
  });
});
