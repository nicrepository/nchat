export type CallType = "audio" | "video";
export type CallStatus = "ringing" | "active" | "declined" | "cancelled" | "timed_out" | "ended";

export interface Call {
  call_id: string;
  request_id: string;
  caller_id: string;
  callee_id: string;
  target_type?: "user" | "channel" | "dm";
  target_id?: string;
  call_type: CallType;
  status: CallStatus;
  version: number;
  created_at: string;
  occurred_at: string;
  expires_at: string;
  accepted_at?: string;
  ended_at?: string;
}

export interface CallEvent {
  type:
    | "call.ringing"
    | "call.accepted"
    | "call.declined"
    | "call.cancelled"
    | "call.timed_out"
    | "call.ended";
  event_id: string;
  target_type: "user" | "channel" | "dm";
  target_id: string;
  call: Call;
}

export interface CallState {
  call: Call | null;
}

export const initialCallState: CallState = { call: null };

const terminalStatuses = new Set<CallStatus>(["declined", "cancelled", "timed_out", "ended"]);

export function isTerminalCall(status: CallStatus): boolean {
  return terminalStatuses.has(status);
}

export function applyCallEvent(state: CallState, event: CallEvent): CallState {
  const current = state.call;
  if (!current) return { call: event.call };
  if (current.call_id !== event.call.call_id) {
    return isTerminalCall(current.status) ? { call: event.call } : state;
  }
  if (isTerminalCall(current.status) || event.call.version <= current.version) return state;
  return { call: event.call };
}

/**
 * Validates and normalizes a bare Call payload — the shape shared by
 * call.admitted, call.resource.synced and every lifecycle event's own `call`
 * field (issue #622 round 2). Does not know about any envelope (event type,
 * target, correlation id); callers that need those validate them separately
 * and independently, exactly like the server-side canonicalizer treats
 * envelope and payload as separate checks.
 */
export function parseCall(value: unknown): Call | null {
  if (!value || typeof value !== "object") return null;
  const call = value as Record<string, unknown>;
  if (
    typeof call.call_id !== "string" ||
    typeof call.request_id !== "string" ||
    typeof call.caller_id !== "string" ||
    (call.call_type !== "audio" && call.call_type !== "video") ||
    !["ringing", "active", "declined", "cancelled", "timed_out", "ended"].includes(
      String(call.status),
    ) ||
    // A malformed version (NaN, negative, or non-integer) must never pass
    // through: resourceCallDiscovery's same-call_id ordering (rule A) treats
    // version as the sole authority — if a NaN version were ever stored as
    // `current`, every later comparison (`candidate > NaN`) would be false
    // unconditionally, wedging that observation so no genuinely newer
    // version could ever win again. Zero itself is a legitimate value — the
    // very first ringing event of a call is version 0 in real usage (see
    // useCallSignaling.test.ts's ringingEvent(0)) — so only negative and
    // non-integer values are rejected.
    !Number.isInteger(call.version) ||
    (call.version as number) < 0 ||
    typeof call.created_at !== "string" ||
    typeof call.occurred_at !== "string" ||
    typeof call.expires_at !== "string"
  ) {
    return null;
  }
  if (call.target_type === "channel" || call.target_type === "dm") {
    if (typeof call.target_id !== "string") return null;
  } else if (
    (call.target_type === "user" || call.target_type === undefined) &&
    typeof call.callee_id !== "string"
  ) {
    return null;
  }
  return {
    ...(call as unknown as Call),
    callee_id: typeof call.callee_id === "string" ? call.callee_id : "",
  };
}

export function parseCallEvent(value: unknown): CallEvent | null {
  if (!value || typeof value !== "object") return null;
  const event = value as Record<string, unknown>;
  if (
    ![
      "call.ringing",
      "call.accepted",
      "call.declined",
      "call.cancelled",
      "call.timed_out",
      "call.ended",
    ].includes(String(event.type)) ||
    !["user", "channel", "dm"].includes(String(event.target_type)) ||
    typeof event.event_id !== "string"
  ) {
    return null;
  }
  const call = parseCall(event.call);
  if (!call) return null;
  // parseCall's own callee_id requirement is keyed off the *payload's own*
  // target_type, which lets a resource payload (target_type channel/dm)
  // through without one. The envelope's target_type is what actually decides
  // routing here, so a "user"-targeted envelope must still be independently
  // required to carry a real callee_id, exactly as before this was factored
  // out into parseCall.
  if (
    event.target_type === "user" &&
    typeof (event.call as Record<string, unknown>).callee_id !== "string"
  ) {
    return null;
  }
  if (
    event.target_type !== "user" &&
    (call.target_type !== event.target_type || call.target_id !== event.target_id)
  ) {
    return null;
  }
  return { ...(value as CallEvent), call };
}
