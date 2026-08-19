export type CallTechnicalEvent =
  | "incoming-shown"
  | "accepted"
  | "rejected"
  | "join-success"
  | "join-failure"
  | "reconnect"
  | "floating-activated"
  | "dedicated-opened"
  | "handoff-start"
  | "handoff-success"
  | "handoff-failure"
  | "ownership-takeover"
  | "duplicate-owner-detected"
  | "end"
  | "track-cleanup";

/** Local observability hook. Deliberately carries no call id, token, SDP, or user content. */
export function emitCallTechnicalEvent(event: CallTechnicalEvent): void {
  window.dispatchEvent(new CustomEvent("nchat:call-technical-event", { detail: { event } }));
}
