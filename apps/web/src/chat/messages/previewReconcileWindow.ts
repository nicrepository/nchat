/**
 * The bounded, backed-off polling window that waits for an inline attachment's
 * preview to finish rendering (RF-31/#464).
 *
 * Mirrors useConversationDetails.ts's ReconcileWindow/reconcileReducer exactly
 * (same shape, same "polled"/"resumed" vocabulary), kept separate rather than
 * shared because the two hooks watch different data — this one a loaded page
 * of messages, that one a destination's file listing — and importing a
 * reducer across hook modules for one struct's worth of bookkeeping would be
 * tighter coupling than the bookkeeping is worth.
 *
 * Only "generation" (waiting for a preview to appear) is needed here, not
 * useConversationDetails.ts's open-ended "revocation": a preview that stops
 * being servable after publication is a scan re-verdict, and that already
 * arrives as attachment_status over the socket.
 */

export interface PreviewReconcileWindow {
  round: number;
  attempt: number;
  target: string;
  progressKey: string;
}

export type PreviewReconcileAction =
  | { type: "polled"; target: string; progressKey: string }
  | { type: "restart" }
  | { type: "resumed" };

export const initialPreviewReconcile: PreviewReconcileWindow = {
  round: 0,
  attempt: 0,
  target: "",
  progressKey: "",
};

export function previewReconcileReducer(
  state: PreviewReconcileWindow,
  action: PreviewReconcileAction,
): PreviewReconcileWindow {
  switch (action.type) {
    case "polled": {
      const sameWindow = state.target === action.target && state.progressKey === action.progressKey;
      return {
        round: state.round + 1,
        attempt: sameWindow ? state.attempt + 1 : 1,
        target: action.target,
        progressKey: action.progressKey,
      };
    }
    case "restart":
      return { ...initialPreviewReconcile, round: state.round + 1 };
    case "resumed":
      return { ...state, round: state.round + 1 };
  }
}
