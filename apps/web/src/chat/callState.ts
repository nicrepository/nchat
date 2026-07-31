export type CallType = "audio" | "video";
export type CallStatus = "ringing" | "active" | "declined" | "cancelled" | "timed_out" | "ended";

export interface Call {
  call_id: string;
  request_id: string;
  caller_id: string;
  callee_id: string;
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
  target_type: "user";
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
    event.target_type !== "user" ||
    typeof event.event_id !== "string" ||
    !event.call ||
    typeof event.call !== "object"
  ) {
    return null;
  }
  const call = event.call as Record<string, unknown>;
  if (
    typeof call.call_id !== "string" ||
    typeof call.request_id !== "string" ||
    typeof call.caller_id !== "string" ||
    typeof call.callee_id !== "string" ||
    (call.call_type !== "audio" && call.call_type !== "video") ||
    !["ringing", "active", "declined", "cancelled", "timed_out", "ended"].includes(
      String(call.status),
    ) ||
    typeof call.version !== "number" ||
    typeof call.created_at !== "string" ||
    typeof call.occurred_at !== "string" ||
    typeof call.expires_at !== "string"
  ) {
    return null;
  }
  return value as CallEvent;
}
