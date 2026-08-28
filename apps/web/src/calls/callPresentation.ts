export type ActivePresentationMode = "active_floating" | "active_dedicated_tab";

export type PresentationMode =
  | "idle"
  | "incoming"
  | "connecting"
  | ActivePresentationMode
  | "handoff_to_tab"
  | "recovering_to_floating"
  | "reconnecting"
  | "ended"
  | "failed";

export interface PresentationState {
  mode: PresentationMode;
  resume?: ActivePresentationMode;
}

export type PresentationEvent =
  | { type: "INCOMING" }
  | { type: "CONNECT" }
  | { type: "CONNECTED"; dedicated: boolean }
  | { type: "HANDOFF_START" }
  | { type: "HANDOFF_ACK" }
  | { type: "HANDOFF_TIMEOUT" }
  | { type: "OWNER_LOST" }
  | { type: "RECOVERED" }
  | { type: "RECONNECTING" }
  | { type: "RECONNECTED" }
  | { type: "END" }
  | { type: "FAIL" }
  | { type: "RESET" };

export const initialPresentation: PresentationState = { mode: "idle" };

export function transition(state: PresentationState, event: PresentationEvent): PresentationState {
  if (event.type === "END" && !["idle", "ended", "failed"].includes(state.mode)) {
    return { mode: "ended" };
  }
  if (event.type === "FAIL" && !["idle", "ended", "failed"].includes(state.mode)) {
    return { mode: "failed" };
  }
  if (event.type === "RESET" && (state.mode === "ended" || state.mode === "failed")) {
    return initialPresentation;
  }

  switch (state.mode) {
    case "idle":
      if (event.type === "INCOMING") return { mode: "incoming" };
      if (event.type === "CONNECT") return { mode: "connecting" };
      break;
    case "incoming":
      if (event.type === "CONNECT") return { mode: "connecting" };
      break;
    case "connecting":
      if (event.type === "CONNECTED") {
        return { mode: event.dedicated ? "active_dedicated_tab" : "active_floating" };
      }
      break;
    case "active_floating":
      if (event.type === "HANDOFF_START") return { mode: "handoff_to_tab" };
      if (event.type === "RECONNECTING") {
        return { mode: "reconnecting", resume: "active_floating" };
      }
      break;
    case "handoff_to_tab":
      if (event.type === "HANDOFF_ACK") return { mode: "active_dedicated_tab" };
      if (event.type === "OWNER_LOST" || event.type === "HANDOFF_TIMEOUT") {
        return { mode: "recovering_to_floating" };
      }
      break;
    case "active_dedicated_tab":
      if (event.type === "OWNER_LOST") return { mode: "recovering_to_floating" };
      if (event.type === "RECONNECTING") {
        return { mode: "reconnecting", resume: "active_dedicated_tab" };
      }
      break;
    case "recovering_to_floating":
      if (event.type === "RECOVERED") return { mode: "active_floating" };
      break;
    case "reconnecting":
      if (event.type === "RECONNECTED" && state.resume) return { mode: state.resume };
      if (event.type === "OWNER_LOST" && state.resume === "active_dedicated_tab") {
        return { mode: "recovering_to_floating" };
      }
      break;
    case "ended":
    case "failed":
      break;
  }

  return state;
}
